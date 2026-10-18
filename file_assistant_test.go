package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fileProjectStub struct {
	calls  []string
	writes int
	diff   string
}

func (p *fileProjectStub) Call(_ context.Context, name string, args, out any) error {
	p.calls = append(p.calls, name)
	var value any
	arguments, _ := args.(map[string]any)
	switch name {
	case "project_get_info":
		value = map[string]any{"project": "ai-challenge-app", "fileToolsVersion": 1, "fileWorkspace": "ai-challenge-app"}
	case "project_search_files":
		value = map[string]any{"evidence": []Evidence{{ID: "search-1", Source: "main.go:7", Text: "POST /api/chat"}}, "truncated": false}
	case "project_search_docs":
		value = map[string]any{"mode": "rag", "evidence": []Evidence{{ID: "knowledge-doc", Source: "docs/rag.md", Text: "RAG использует локальные эмбеддинги embeddinggemma."}}}
	case "project_read_file":
		path, _ := arguments["path"].(string)
		start := 1
		end := 2
		total := 2
		if path == "README.md" {
			total = 4
			if arguments["startLine"] == float64(3) {
				start = 3
				end = 4
			}
		}
		value = map[string]any{"id": "file:" + path, "source": path, "text": "POST /api/chat", "sha256": "hash:" + path, "startLine": start, "endLine": end, "totalLines": total, "truncated": end < total}
	case "project_write_file", "project_preview_diff":
		if name == "project_write_file" {
			p.writes++
		}
		diff := p.diff
		if diff == "" {
			diff = "--- /dev/null\n+++ b/docs/API.md\n@@ -0,0 +1,1 @@\n+# API\n"
		}
		value = map[string]any{"path": arguments["path"], "saved": name == "project_write_file", "changed": true, "sha256": "saved-hash", "diff": diff}
	default:
		return errors.New("unexpected tool: " + name)
	}
	data, _ := json.Marshal(value)
	return json.Unmarshal(data, out)
}

func fileAssistantFor(t *testing.T, p *fileProjectStub, g Generator) (*Assistant, *Store) {
	a, s := assistantFor(t, &projectStub{}, g)
	a.Project = func(context.Context) (Caller, func(), error) { return p, func() {}, nil }
	return a, s
}

func TestFileAssistantChoosesToolsAcrossThreeFilesAndPersistsDiff(t *testing.T) {
	p := &fileProjectStub{diff: "--- /dev/null\n+++ b/docs/API.md\n@@ -0,0 +1,4000 @@\n" + strings.Repeat("+Строка\n", 4000)}
	step := 0
	g := generatorFunc(func(_ context.Context, m []ModelMessage) (string, error) {
		step++
		if !strings.Contains(m[0].Content, "project_write_file") {
			t.Fatal("no file tool catalog")
		}
		if !strings.Contains(m[0].Content, "Слова «проект», «приложение», «код», «документация» по умолчанию означают ai-challenge-app") || !strings.Contains(m[1].Content, `"fileWorkspace":"ai-challenge-app"`) {
			t.Fatal("assistant is not scoped to the supported ai-challenge-app project")
		}
		switch step {
		case 1:
			return `{"toolCalls":[{"name":"project_search_files","arguments":{"query":"/api/chat"}}]}`, nil
		case 2:
			if !strings.Contains(m[len(m)-1].Content, "main.go:7") {
				t.Fatal("search results not returned to model")
			}
			return `{"toolCalls":[{"name":"project_read_file","arguments":{"path":"main.go"}},{"name":"project_read_file","arguments":{"path":"tools.go"}},{"name":"project_read_file","arguments":{"path":"web/app.js"}}]}`, nil
		case 3:
			return `{"toolCalls":[{"name":"project_write_file","arguments":{"path":"docs/API.md","content":"# API\n","expectedSha256":"missing"}}]}`, nil
		default:
			return `{"claims":[{"text":"Проверены обработчик и клиент API.","evidence":[{"id":"file:main.go","quote":"POST /api/chat"}]}]}`, nil
		}
	})
	a, s := fileAssistantFor(t, p, g)
	r, err := a.Ask(context.Background(), "SUP-004", "Изучи API CRM и сохрани документацию")
	if err != nil || p.writes != 1 || !strings.Contains(r.Answer, "Сохранено: docs/API.md") || !strings.Contains(r.Answer, "+++ b/docs/API.md") || r.Trace.Retrieval != "project_files" {
		t.Fatalf("reply: %+v writes=%d err=%v", r, p.writes, err)
	}
	ticket, _ := s.Ticket(context.Background(), "SUP-004")
	if len(ticket.Messages) != 3 || ticket.Messages[2].Content != r.Answer || len(ticket.Messages[2].Trace.ModelContext.Evidence) < 4 {
		t.Fatal("answer/diff/context were not persisted")
	}
}

func TestFileAssistantPreservesWriteResultWhenModelFails(t *testing.T) {
	p := &fileProjectStub{}
	step := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, store := fileAssistantFor(t, p, generatorFunc(func(context.Context, []ModelMessage) (string, error) {
		step++
		switch step {
		case 1:
			return `{"toolCalls":[{"name":"project_read_file","arguments":{"path":"main.go"}},{"name":"project_read_file","arguments":{"path":"tools.go"}}]}`, nil
		case 2:
			return `{"toolCalls":[{"name":"project_write_file","arguments":{"path":"docs/API.md","content":"# API","expectedSha256":"missing"}}]}`, nil
		default:
			cancel()
			return "", errors.New("deepseek_unavailable")
		}
	}))
	r, err := a.Ask(ctx, "SUP-004", "Сохрани описание API")
	if err != nil || !strings.Contains(r.Answer, "Сохранено: docs/API.md") || !strings.Contains(r.Answer, "+++ b/docs/API.md") {
		t.Fatalf("write result lost: %+v %v", r, err)
	}
	ticket, _ := store.Ticket(context.Background(), "SUP-004")
	if ticket.Messages[len(ticket.Messages)-1].Content != r.Answer {
		t.Fatal("write result lost after context cancellation")
	}
}

func TestFileAssistantRejectsUnknownToolsAndUnreadWrites(t *testing.T) {
	p := &fileProjectStub{}
	step := 0
	a, _ := fileAssistantFor(t, p, generatorFunc(func(_ context.Context, m []ModelMessage) (string, error) {
		step++
		if step == 1 {
			return `{"toolCalls":[{"name":"crm_delete_ticket","arguments":{"id":"SUP-004"}},{"name":"project_write_file","arguments":{"path":"docs/API.md","content":"x","expectedSha256":"missing"}}]}`, nil
		}
		if !strings.Contains(m[len(m)-2].Content, "tool_not_allowed") || !strings.Contains(m[len(m)-1].Content, "two distinct files") {
			t.Fatal("tool errors not sent back")
		}
		return `{"claims":[],"followUp":"Какие документы нужно подготовить?"}`, nil
	}))
	_, err := a.Ask(context.Background(), "", "Проверь проект")
	if err != nil || p.writes != 0 || len(p.calls) != 1 {
		t.Fatalf("unauthorized call: %v %+v", err, p.calls)
	}
}

func TestFileAssistantRequiresWholeDestinationAndAllowsPagedRead(t *testing.T) {
	p := &fileProjectStub{}
	step := 0
	a, _ := fileAssistantFor(t, p, generatorFunc(func(_ context.Context, m []ModelMessage) (string, error) {
		step++
		switch step {
		case 1:
			return `{"toolCalls":[{"name":"project_read_file","arguments":{"path":"main.go"}},{"name":"project_read_file","arguments":{"path":"README.md"}}]}`, nil
		case 2:
			return `{"toolCalls":[{"name":"project_write_file","arguments":{"path":"README.md","content":"x","expectedSha256":"hash:README.md"}}]}`, nil
		case 3:
			if !strings.Contains(m[len(m)-1].Content, "entire destination") {
				t.Fatal("partial document allowed")
			}
			return `{"toolCalls":[{"name":"project_read_file","arguments":{"path":"README.md","startLine":3}},{"name":"project_preview_diff","arguments":{"path":"README.md","content":"x","expectedSha256":"hash:README.md"}}]}`, nil
		default:
			return `{"claims":[],"followUp":"Сохранить предложенный документ?"}`, nil
		}
	}))
	r, err := a.Ask(context.Background(), "", "Подготовь diff README, не сохраняй")
	if err != nil || p.writes != 0 || !strings.Contains(r.Answer, "Предпросмотр: README.md") {
		t.Fatalf("preview: %+v %v", r, err)
	}
}

func TestFileAssistantRepairsShortStatusCodeCitations(t *testing.T) {
	p := &fileProjectStub{}
	step := 0
	a, _ := fileAssistantFor(t, p, generatorFunc(func(_ context.Context, messages []ModelMessage) (string, error) {
		step++
		switch step {
		case 1:
			return `{"toolCalls":[{"name":"project_read_file","arguments":{"path":"main.go"}}]}`, nil
		case 2:
			return `{"claims":[{"text":"Маршрут чата найден.","evidence":[{"id":"file:main.go","quote":"api"}]}]}`, nil
		default:
			if !strings.Contains(messages[len(messages)-1].Content, "МИНИМУМ 4") {
				t.Fatal("repair feedback lacks the failing citation constraint")
			}
			return `{"claims":[{"text":"Маршрут чата найден.","evidence":[{"id":"file:main.go","quote":"POST /api/chat"}]}]}`, nil
		}
	}))
	r, err := a.Ask(context.Background(), "", "Как работает чат?")
	if err != nil || len(r.Sources) != 1 || r.Trace.Attempts != 3 {
		t.Fatalf("repair failed: %+v %v", r, err)
	}
}

func TestFileAssistantKeepsExistingRAGKnowledgeFlow(t *testing.T) {
	p := &fileProjectStub{}
	step := 0
	a, _ := fileAssistantFor(t, p, generatorFunc(func(_ context.Context, messages []ModelMessage) (string, error) {
		step++
		if step == 1 {
			return `{"toolCalls":[{"name":"project_search_docs","arguments":{"question":"Как устроен RAG?","limit":3}}]}`, nil
		}
		if !strings.Contains(messages[len(messages)-1].Content, "knowledge-doc") {
			t.Fatal("RAG evidence was not delivered")
		}
		return `{"claims":[{"text":"Для RAG используются локальные эмбеддинги.","evidence":[{"id":"knowledge-doc","quote":"локальные эмбеддинги embeddinggemma"}]}]}`, nil
	}))
	r, err := a.Ask(context.Background(), "", "Как устроен RAG в AI Challenge?")
	if err != nil || r.Trace.Retrieval != "rag" || len(r.Sources) != 1 || r.Sources[0].Source != "docs/rag.md" || p.writes != 0 {
		t.Fatalf("knowledge regression: %+v %v", r, err)
	}
}
