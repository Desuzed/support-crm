package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "crm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func testCRM(t *testing.T, s *Store) *MCPClient {
	t.Helper()
	c, err := NewCRM(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestCRMPersistenceAndTicketIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crm.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	tickets, err := s.Tickets(ctx)
	if err != nil || len(tickets) != 6 {
		t.Fatalf("seed: %d %v", len(tickets), err)
	}
	user, err := s.AddUser(ctx, "Реальный пользователь", map[string]any{"device": "Mac"})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := s.Create(ctx, Ticket{UserID: user.ID, Title: "Мой вопрос", Description: "Что делает MCP?"})
	if err != nil || ticket.Demo {
		t.Fatalf("create: %v %v", ticket, err)
	}
	if err = s.AddMessage(ctx, "SUP-001", "operator", "Проверили порт", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Status(ctx, ticket.ID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if err = s.Status(ctx, ticket.ID, "invalid"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reloaded, err := s.Ticket(ctx, ticket.ID)
	if err != nil || reloaded.Status != "resolved" || len(reloaded.Messages) != 1 {
		t.Fatalf("persist: %v %v", reloaded, err)
	}
	other, err := s.Ticket(ctx, "SUP-002")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range other.Messages {
		if strings.Contains(m.Content, "Проверили порт") {
			t.Fatal("ticket data leaked")
		}
	}
}
func TestActualCRMToolProtocol(t *testing.T) {
	c := testCRM(t, testStore(t))
	ctx := context.Background()
	tools, err := c.session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 7 {
		t.Fatalf("catalog: %v %v", tools, err)
	}
	var ticket Ticket
	if err := c.Call(ctx, "crm_get_ticket", map[string]string{"id": "SUP-004"}, &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.Environment["strategy"] != "Sliding Window" || !ticket.Demo {
		t.Fatalf("wrong context: %v", ticket)
	}
	if err := c.Call(ctx, "crm_get_ticket", map[string]string{"id": "unknown"}, &ticket); err == nil {
		t.Fatal("unknown ticket accepted")
	}
	if err := c.Call(ctx, "crm_add_message", map[string]string{"id": "SUP-004", "role": "operator", "content": "Проверено"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "crm_create_ticket", map[string]string{"userId": "local-operator", "title": "Новый вопрос", "description": "Что такое RAG?"}, &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.Demo || len(ticket.Messages) != 1 {
		t.Fatal("new ticket was not persisted")
	}
}

type projectStub struct {
	failSearch bool
	calls      []string
}

func (p *projectStub) Call(_ context.Context, name string, args, out any) error {
	p.calls = append(p.calls, name)
	var value any
	switch name {
	case "project_get_info":
		value = map[string]any{"indexStatus": "ready", "commit": "test-revision"}
	case "project_search_docs":
		if p.failSearch {
			return errors.New("index_stale")
		}
		value = map[string]any{"mode": "rag", "evidence": []Evidence{{ID: "doc-1", Source: "docs/memory.md", Text: "Sliding Window отправляет последние N сообщений. Архив истории сохраняется."}}}
	case "project_read_document":
		value = Evidence{ID: "fallback-1", Source: "docs/troubleshooting.md", Text: "Пересоберите индекс командой /reindex."}
	default:
		return errors.New("unexpected tool")
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}

type generatorFunc func(context.Context, []ModelMessage) (string, error)

func (g generatorFunc) Generate(ctx context.Context, m []ModelMessage) (string, error) {
	return g(ctx, m)
}
func assistantFor(t *testing.T, p *projectStub, g Generator) (*Assistant, *Store) {
	s := testStore(t)
	c := testCRM(t, s)
	return &Assistant{CRM: c, Model: g, ModelName: "deepseek-flash", SaveTrace: s.UpdateMessageTrace, Project: func(context.Context) (Caller, func(), error) { return p, func() {}, nil }}, s
}
func TestAssistantUsesTicketMCPAndExactDocumentCitations(t *testing.T) {
	p := &projectStub{}
	g := generatorFunc(func(_ context.Context, m []ModelMessage) (string, error) {
		if !strings.Contains(m[1].Content, "Sliding Window") || strings.Contains(m[1].Content, "GitHub HTTP 401") {
			t.Fatal("incorrect ticket context")
		}
		return `{"claims":[{"text":"Окно отправляет последние сообщения; архив сохраняется.","evidence":[{"id":"doc-1","quote":"Архив истории сохраняется."}]}],"followUp":"Нужно сохранить конкретный факт?"}`, nil
	})
	a, s := assistantFor(t, p, g)
	r, err := a.Ask(context.Background(), "SUP-004", "Почему модель забыла начало?")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) != 1 || r.Trace.CRM != "mcp" || r.Trace.Retrieval != "rag" || r.Trace.Attempts != 1 {
		t.Fatalf("reply: %+v", r)
	}
	ticket, _ := s.Ticket(context.Background(), "SUP-004")
	if len(ticket.Messages) != 3 || ticket.Messages[2].Role != "assistant" {
		t.Fatal("conversation not saved")
	}
	other, _ := s.Ticket(context.Background(), "SUP-001")
	if len(other.Messages) != 1 {
		t.Fatal("other conversation changed")
	}
}
func TestAssistantRejectsInventedCitations(t *testing.T) {
	attempts := 0
	a, _ := assistantFor(t, &projectStub{}, generatorFunc(func(context.Context, []ModelMessage) (string, error) {
		attempts++
		return `{"claims":[{"text":"Неподтверждённое утверждение","evidence":[{"id":"doc-1","quote":"Этой цитаты нет в документе"}]}]}`, nil
	}))
	r, err := a.Ask(context.Background(), "SUP-004", "Где история?")
	if err != nil || attempts != 2 || len(r.Sources) != 0 || strings.Contains(r.Answer, "Неподтверждённое утверждение") {
		t.Fatalf("unverified response escaped: %+v %v", r, err)
	}
}
func TestFallbackAndOfflineBehaviour(t *testing.T) {
	p := &projectStub{failSearch: true}
	a, s := assistantFor(t, p, generatorFunc(func(context.Context, []ModelMessage) (string, error) {
		return `{"claims":[{"text":"Нужна переиндексация.","evidence":[{"id":"fallback-1","quote":"Пересоберите индекс"}]}]}`, nil
	}))
	r, err := a.Ask(context.Background(), "SUP-002", "Как восстановить индекс?")
	if err != nil || r.Trace.Retrieval != "direct_documents" {
		t.Fatalf("fallback: %+v %v", r, err)
	}
	a.Project = func(context.Context) (Caller, func(), error) { return nil, func() {}, errors.New("offline") }
	r, err = a.Ask(context.Background(), "SUP-003", "Что делать?")
	if err != nil || r.Trace.Project != "unavailable" {
		t.Fatalf("offline: %+v %v", r, err)
	}
	ticket, _ := s.Ticket(context.Background(), "SUP-003")
	if len(ticket.Messages) != 3 {
		t.Fatal("offline conversation not preserved")
	}
}
func TestDeepSeekKeyStaysServerSideAndErrorsAreSanitized(t *testing.T) {
	key := "private-test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Error("key missing from Authorization")
		}
		b, _ := io.ReadAll(r.Body)
		if bytes.Contains(b, []byte(key)) {
			t.Error("key leaked into prompt")
		}
		var in map[string]any
		_ = json.Unmarshal(b, &in)
		if in["model"] != "deepseek-flash" {
			t.Error("wrong model")
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(key + " raw provider body"))
	}))
	defer server.Close()
	d := NewDeepSeek(key, "deepseek-flash")
	d.Endpoint = server.URL
	_, err := d.Generate(context.Background(), []ModelMessage{{Role: "user", Content: "test"}})
	if err == nil || err.Error() != "deepseek_http_401" || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe error: %v", err)
	}
}
func TestHTTPAuthenticationAndCSRF(t *testing.T) {
	c := testCRM(t, testStore(t))
	token := strings.Repeat("t", 40)
	a := &App{Config: Config{AccessToken: token}, CRM: c}
	h := a.handler()
	req := httptest.NewRequest("GET", "http://localhost/api/tickets", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("anonymous CRM access allowed")
	}
	req = httptest.NewRequest("POST", "http://localhost/api/login", strings.NewReader(marshal(map[string]string{"token": token})))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	req = httptest.NewRequest("GET", "http://localhost/api/tickets", nil)
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "SUP-001") {
		t.Fatal("authenticated CRM unavailable")
	}
	req = httptest.NewRequest("PATCH", "http://localhost/api/tickets/SUP-001", strings.NewReader(`{"status":"resolved"}`))
	req.AddCookie(cookies[0])
	req.Header.Set("Origin", "http://evil.invalid")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	req = httptest.NewRequest("GET", "http://localhost/.env", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("non-embedded file served")
	}
}

func TestSessionCookieForHTTPSProxyAndLocalAccess(t *testing.T) {
	for _, test := range []struct {
		name, url        string
		secure, expected bool
	}{
		{"local HTTP", "http://localhost", false, false},
		{"HTTPS proxy", "http://localhost", true, true},
		{"direct HTTPS", "https://localhost", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := strings.Repeat("t", 40)
			h := (&App{Config: Config{AccessToken: token, SecureCookies: test.secure}}).handler()
			for _, endpoint := range []string{"login", "logout"} {
				req := httptest.NewRequest("POST", test.url+"/api/"+endpoint, strings.NewReader(marshal(map[string]string{"token": token})))
				// Untrusted forwarded headers cannot enable or disable this setting.
				req.Header.Set("X-Forwarded-Proto", "https")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				cookies := w.Result().Cookies()
				if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Secure != test.expected || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
					t.Fatalf("%s cookie: status=%d cookies=%v", endpoint, w.Code, cookies)
				}
			}
		})
	}
}
