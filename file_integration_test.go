package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An installed bridge is optional in ordinary CRM CI. This test exercises the
// production HTTP -> model protocol -> remote MCP -> actual filesystem chain,
// with a local deterministic model endpoint and disposable project files.
func TestFileAssistantEndToEnd(t *testing.T) {
	binary := os.Getenv("PROJECT_FILES_TEST_BINARY")
	if binary == "" {
		t.Skip("set PROJECT_FILES_TEST_BINARY to the built cmd/supportmcp executable")
	}
	root := t.TempDir()
	for p, text := range map[string]string{
		"README.md": "# Fixture CRM\n", "main.go": "package main\n// DELETE /api/tickets/{id}\n",
		"tools.go": "package main\n// crm_delete_ticket\n", "web/app.js": "// DELETE /api/tickets/{id}\n",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	token := strings.Repeat("integration-token-", 3)
	cmd := exec.Command(binary, "-root", root, "-addr", addr)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SUPPORT_MCP_TOKEN=" + token, "SUPPORT_FILES_ROOT=" + root}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	url := "http://" + addr + "/mcp"
	var project *MCPClient
	for attempt := 0; attempt < 40; attempt++ {
		project, err = ConnectProject(context.Background(), url, token)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer project.Close()
	const document = "# Ticket deletion\n\nDELETE /api/tickets/{id} uses crm_delete_ticket.\n"
	mode := "usage"
	step := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []ModelMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		step++
		messages := payload.Messages
		call := func(name string, args map[string]any) fileToolCall { return fileToolCall{Name: name, Arguments: args} }
		var output any
		all := marshal(messages)
		switch step {
		case 1:
			output = map[string]any{"toolCalls": []fileToolCall{call("project_list_files", map[string]any{})}}
		case 2:
			output = map[string]any{"toolCalls": []fileToolCall{
				call("project_search_files", map[string]any{"query": "DELETE"}),
				call("project_read_file", map[string]any{"path": "main.go"}),
				call("project_read_file", map[string]any{"path": "tools.go"}),
				call("project_read_file", map[string]any{"path": "web/app.js"}),
			}}
		default:
			if mode == "document" && step == 3 {
				if strings.Contains(all, "docs/ticket-deletion.md") {
					output = map[string]any{"toolCalls": []fileToolCall{call("project_read_file", map[string]any{"path": "docs/ticket-deletion.md"})}}
				} else {
					output = map[string]any{"toolCalls": []fileToolCall{call("project_write_file", map[string]any{"path": "docs/ticket-deletion.md", "content": document, "expectedSha256": "missing"})}}
				}
			} else if mode == "document" && step == 4 && strings.Contains(messages[len(messages)-1].Content, `"name":"project_read_file"`) {
				var last struct {
					ToolResult struct {
						Result map[string]any `json:"result"`
					} `json:"toolResult"`
				}
				_ = json.Unmarshal([]byte(messages[len(messages)-1].Content), &last)
				output = map[string]any{"toolCalls": []fileToolCall{call("project_write_file", map[string]any{"path": "docs/ticket-deletion.md", "content": document, "expectedSha256": last.ToolResult.Result["sha256"]})}}
			} else {
				id := ""
				for _, message := range messages {
					var data struct {
						Evidence []Evidence `json:"evidence"`
					}
					_ = json.Unmarshal([]byte(message.Content), &data)
					for _, e := range data.Evidence {
						if strings.HasPrefix(e.Source, "main.go:") && strings.Contains(e.Text, "DELETE") {
							id = e.ID
						}
					}
				}
				if id == "" {
					t.Error("no actual source from MCP")
				}
				output = map[string]any{"claims": []any{map[string]any{"text": "Удаление проходит через API, MCP и браузер.", "evidence": []any{map[string]any{"id": id, "quote": "DELETE /api/tickets/{id}"}}}}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": ModelMessage{Role: "assistant", Content: marshal(output)}, "finish_reason": "stop"}}})
	}))
	defer provider.Close()
	model := NewDeepSeek("local-fixture-key", "fixture-model")
	model.Endpoint = provider.URL
	store := testStore(t)
	crm := testCRM(t, store)
	assistant := &Assistant{CRM: crm, Model: model, ModelName: "fixture-model", SaveTrace: store.UpdateMessageTrace, Project: func(context.Context) (Caller, func(), error) { return project, func() {}, nil }}
	app := &App{Config: Config{AccessToken: token}, CRM: crm, Assistant: assistant, general: make(chan struct{}, 4)}
	server := httptest.NewServer(app.handler())
	defer server.Close()
	for _, scenario := range []string{"usage", "document", "document"} {
		mode = scenario
		step = 0
		req, _ := http.NewRequest("POST", server.URL+"/api/chat", strings.NewReader(marshal(map[string]string{"question": "Проверь удаление обращений; " + scenario, "ticketId": "SUP-004"})))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var reply Reply
		if resp.StatusCode != 200 || json.Unmarshal(data, &reply) != nil {
			t.Fatalf("HTTP %d: %s", resp.StatusCode, data)
		}
		reads := map[string]bool{}
		writeCalls := 0
		for _, call := range reply.Trace.MCPCalls {
			if call.Tool == "project_read_file" && call.Error == "" {
				reads[call.Arguments.(map[string]any)["path"].(string)] = true
			}
			if call.Tool == "project_write_file" {
				writeCalls++
				if call.Error != "" {
					t.Fatal(call.Error)
				}
			}
		}
		if len(reads) < 3 || len(reply.Sources) == 0 || reply.Trace.Retrieval != "project_files" {
			t.Fatalf("incomplete flow: %+v", reply)
		}
		if scenario == "usage" && writeCalls != 0 {
			t.Fatal("read-only task wrote a file")
		}
		if scenario == "document" {
			actual, err := os.ReadFile(filepath.Join(root, "docs/ticket-deletion.md"))
			if err != nil || string(actual) != document || writeCalls != 1 {
				t.Fatalf("saved document: %q %v, writes=%d", actual, err, writeCalls)
			}
		}
		ticket, _ := store.Ticket(context.Background(), "SUP-004")
		last := ticket.Messages[len(ticket.Messages)-1]
		if last.Content != reply.Answer || len(last.Trace.MCPCalls) != len(reply.Trace.MCPCalls) {
			t.Fatal("complete reply and MCP journal were not persisted")
		}
		t.Logf("%s: %d files, %d MCP calls, writes=%d, answer=%q", scenario, len(reads), len(reply.Trace.MCPCalls), writeCalls, reply.Answer)
	}
}
