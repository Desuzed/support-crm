package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteTicketIsAtomicAndPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crm.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	if err := store.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctx, "SUP-001", "assistant", "Saved reply", nil, &Trace{Model: "test", MCPCalls: []MCPCall{{Tool: "crm_get_ticket"}}}); err != nil {
		t.Fatal(err)
	}
	// A failure after removing messages must roll back the whole deletion.
	if _, err := store.db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON tickets BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTicket(ctx, "SUP-001"); err == nil {
		t.Fatal("injected deletion failure ignored")
	}
	ticket, err := store.Ticket(ctx, "SUP-001")
	if err != nil || len(ticket.Messages) != 2 || ticket.Messages[1].Trace == nil {
		t.Fatalf("partial deletion: ticket=%+v err=%v", ticket, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_delete`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTicket(ctx, "SUP-001"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ticket(ctx, "SUP-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted ticket reappeared: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM messages WHERE ticket_id='SUP-001'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("messages or journal retained: count=%d err=%v", count, err)
	}
	other, err := store.Ticket(ctx, "SUP-002")
	if err != nil || len(other.Messages) != 1 {
		t.Fatalf("unrelated ticket changed: %v", err)
	}
	if _, err := store.User(ctx, "local-operator"); err != nil {
		t.Fatalf("user deleted with ticket: %v", err)
	}
	if err := store.DeleteTicket(ctx, "SUP-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing ticket: %v", err)
	}
}

func TestDeleteTicketHTTPUsesMCPAndProtectsActiveReply(t *testing.T) {
	store := testStore(t)
	client := testCRM(t, store)
	token := strings.Repeat("t", 40)
	app := &App{Config: Config{AccessToken: token}, CRM: client}
	handler := app.handler()
	request := func(id, authorization, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/tickets/"+id, nil)
		if authorization != "" {
			req.Header.Set("Authorization", "Bearer "+authorization)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := request("SUP-001", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous deletion: %d", response.Code)
	}
	if response := request("SUP-001", token, "http://evil.invalid"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin deletion: %d", response.Code)
	}
	gate := app.ticketGate("SUP-001")
	gate <- struct{}{}
	if response := request("SUP-001", token, ""); response.Code != http.StatusConflict {
		t.Fatalf("deletion during reply: %d", response.Code)
	}
	<-gate
	if _, err := store.Ticket(context.Background(), "SUP-001"); err != nil {
		t.Fatalf("rejected request deleted data: %v", err)
	}
	otherGate := app.ticketGate("SUP-002")
	otherGate <- struct{}{}
	defer func() { <-otherGate }()
	if response := request("SUP-001", token, "http://localhost"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":true`) {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	if _, err := store.Ticket(context.Background(), "SUP-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MCP deletion did not reach SQLite: %v", err)
	}
	if response := request("SUP-001", token, ""); response.Code != http.StatusNotFound {
		t.Fatalf("repeated deletion: %d", response.Code)
	}
	if _, err := store.Ticket(context.Background(), "SUP-002"); err != nil {
		t.Fatalf("unrelated ticket deleted: %v", err)
	}
	catalog, err := client.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog.Tools {
		if tool.Name == "crm_delete_ticket" {
			if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint || tool.Annotations.ReadOnlyHint {
				t.Fatal("delete tool not marked destructive")
			}
			return
		}
	}
	t.Fatal("delete MCP tool missing")
}
