package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestJournalCapturesActualProtocolResultsAndErrors(t *testing.T) {
	client := testCRM(t, testStore(t))
	ctx, journal := withJournal(context.Background(), nil)
	var ticket Ticket
	if err := client.Call(ctx, "crm_get_ticket", map[string]string{"id": "SUP-004"}, &ticket); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "crm_get_ticket", map[string]string{"id": "missing"}, &ticket); err == nil {
		t.Fatal("expected tool error")
	}
	if err := client.Call(ctx, "crm_add_message", map[string]string{"id": "SUP-004", "role": "operator", "content": "Проверили контекст"}, nil); err != nil {
		t.Fatal(err)
	}
	// A separate request sharing the same connection must not enter this journal.
	if err := client.Call(context.Background(), "crm_get_ticket", map[string]string{"id": "SUP-006"}, &ticket); err != nil {
		t.Fatal(err)
	}
	calls := journal.snapshot()
	if len(calls) != 3 {
		t.Fatalf("wrong calls: %+v", calls)
	}
	if calls[0].Server != "crm" || calls[0].Arguments.(map[string]any)["id"] != "SUP-004" || calls[0].Result.(map[string]any)["id"] != "SUP-004" {
		t.Fatal("actual context not recorded")
	}
	if calls[1].Error == "" || calls[1].Result == nil {
		t.Fatal("tool error or returned error content missing")
	}
	if calls[2].Result.(map[string]any)["saved"] != true {
		t.Fatal("result lost when caller did not request output")
	}
	for i, call := range calls {
		if call.Sequence != i+1 || call.StartedAt == "" || call.DurationMS < 0 {
			t.Fatal("invalid timing/order")
		}
	}
}

func TestJournalPersistsFinalSaveAndOmitsHistoricalJournals(t *testing.T) {
	a, store := assistantFor(t, &projectStub{}, generatorFunc(func(context.Context, []ModelMessage) (string, error) {
		return `{"claims":[{"text":"Архив сохраняется.","evidence":[{"id":"doc-1","quote":"Архив истории сохраняется."}]}]}`, nil
	}))
	ctx := context.Background()
	for turn := 0; turn < 2; turn++ {
		reply, err := a.Ask(ctx, "SUP-004", "Что происходит с историей?")
		if err != nil {
			t.Fatal(err)
		}
		// Project is a stub here; the four CRM operations still use the actual SDK.
		calls := reply.Trace.MCPCalls
		if len(calls) != 4 || calls[3].Tool != "crm_add_message" || calls[3].Arguments.(map[string]any)["role"] != "assistant" || calls[3].Result.(map[string]any)["saved"] != true {
			t.Fatalf("final write missing: %+v", calls)
		}
		ticket, err := store.Ticket(ctx, "SUP-004")
		if err != nil {
			t.Fatal(err)
		}
		saved := ticket.Messages[len(ticket.Messages)-1].Trace
		if saved == nil || len(saved.MCPCalls) != len(calls) || saved.MCPCalls[3].Result.(map[string]any)["saved"] != true {
			t.Fatal("complete journal did not survive CRM reload")
		}
		if saved.ModelContext == nil || saved.ModelContext.TicketID != "SUP-004" || len(saved.ModelContext.Evidence) == 0 {
			t.Fatal("prepared context missing")
		}
		if !calls[3].OmittedNestedLogs {
			t.Fatal("saving trace recursively copied itself")
		}
		if turn == 1 {
			messages := calls[0].Result.(map[string]any)["messages"].([]any)
			priorTrace := messages[2].(map[string]any)["trace"].(map[string]any)
			if _, ok := priorTrace["mcpCalls"].(string); !ok || !calls[0].OmittedNestedLogs {
				t.Fatal("historical journal recursively copied")
			}
		}
	}
}

func TestJournalMasksSecretsAndBoundsUnicodeResults(t *testing.T) {
	secret := "actual-test-private-token-123456"
	j := &MCPJournal{secrets: []string{secret}}
	data, truncated, omitted := j.prepare(map[string]any{
		"api_key": "another-private-key", "nested": map[string]any{"password": "private-password"},
		"note": "copied: " + secret, "header": "Bearer unknown-token-123456789", "deepseekConfigured": true,
		"trace": map[string]any{"mcpCalls": []any{map[string]any{"secret": secret}}},
	})
	b, _ := json.Marshal(data)
	for _, value := range []string{secret, "another-private-key", "private-password", "unknown-token-123456789"} {
		if strings.Contains(string(b), value) {
			t.Fatal("credential leaked")
		}
	}
	if truncated || !omitted || data.(map[string]any)["deepseekConfigured"] != true {
		t.Fatal("wrong masking metadata")
	}
	large, truncated, _ := j.prepare(map[string]any{"text": strings.Repeat("Документ", 20000)})
	if !truncated || !utf8.ValidString(large.(map[string]any)["preview"].(string)) {
		t.Fatal("large Unicode result was not safely bounded")
	}
}

func TestJournalIsolatesConcurrentRequests(t *testing.T) {
	client := testCRM(t, testStore(t))
	var group sync.WaitGroup
	for _, id := range []string{"SUP-001", "SUP-004"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			ctx, j := withJournal(context.Background(), nil)
			var ticket Ticket
			if err := client.Call(ctx, "crm_get_ticket", map[string]string{"id": id}, &ticket); err != nil {
				t.Error(err)
				return
			}
			calls := j.snapshot()
			if len(calls) != 1 || calls[0].Arguments.(map[string]any)["id"] != id || calls[0].Result.(map[string]any)["id"] != id {
				t.Error("cross-request journal contamination")
			}
		}(id)
	}
	group.Wait()
}

func fallbackProjectClient(t *testing.T) *MCPClient {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "project-test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "project_get_info"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]string, error) {
		return nil, map[string]string{"indexStatus": "index_stale"}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "project_search_docs"}, func(context.Context, *mcp.CallToolRequest, struct {
		Question string `json:"question"`
		Limit    int    `json:"limit"`
	}) (*mcp.CallToolResult, map[string]any, error) { return nil, nil, errors.New("index_stale") })
	mcp.AddTool(server, &mcp.Tool{Name: "project_read_document"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
		Path string `json:"path"`
	}) (*mcp.CallToolResult, Evidence, error) { return nil, Evidence{ID: "fallback-1", Source: in.Path, Text: "Пересоберите индекс командой /reindex."}, nil })
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	c := &MCPClient{session: cs, server: ss, service: "project"}
	t.Cleanup(c.Close)
	return c
}

func TestJournalShowsFailedRAGThenDirectDocumentFallback(t *testing.T) {
	a, store := assistantFor(t, &projectStub{}, generatorFunc(func(context.Context, []ModelMessage) (string, error) {
		return `{"claims":[{"text":"Нужна переиндексация.","evidence":[{"id":"fallback-1","quote":"Пересоберите индекс"}]}]}`, nil
	}))
	client := fallbackProjectClient(t)
	a.Project = func(context.Context) (Caller, func(), error) { return client, func() {}, nil }
	reply, err := a.Ask(context.Background(), "SUP-002", "Как восстановить поиск?")
	if err != nil {
		t.Fatal(err)
	}
	if reply.Trace.Retrieval != "direct_documents" || len(reply.Trace.MCPCalls) != 8 {
		t.Fatalf("unexpected journal: %+v", reply.Trace)
	}
	calls := reply.Trace.MCPCalls
	if calls[4].Tool != "project_search_docs" || calls[4].Error == "" || calls[5].Tool != "project_read_document" || calls[5].Error != "" || calls[6].Tool != "project_read_document" {
		t.Fatal("RAG failure/fallback order is wrong")
	}
	if calls[4].Server != "project" || calls[4].Arguments.(map[string]any)["question"] == "" || calls[5].Result.(map[string]any)["text"] == "" {
		t.Fatal("search request or actual document return missing")
	}
	ticket, _ := store.Ticket(context.Background(), "SUP-002")
	if len(ticket.Messages[len(ticket.Messages)-1].Trace.MCPCalls) != 8 {
		t.Fatal("fallback journal not persisted")
	}
}
