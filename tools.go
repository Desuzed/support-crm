package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}

type CreateTicketInput struct {
	UserID      string         `json:"userId"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Category    string         `json:"category,omitempty"`
	Priority    string         `json:"priority,omitempty"`
	Environment map[string]any `json:"environment,omitempty"`
	Context     map[string]any `json:"context,omitempty"`
}
type DeleteTicketResult struct {
	Deleted bool `json:"deleted"`
}
type MCPClient struct {
	session *mcp.ClientSession
	server  *mcp.ServerSession
	service string
}

func (c *MCPClient) Close() {
	if c.session != nil {
		_ = c.session.Close()
	}
	if c.server != nil {
		_ = c.server.Close()
	}
}
func (c *MCPClient) Call(ctx context.Context, name string, args, out any) (callErr error) {
	start := time.Now()
	var returned any
	if recorder := journalFrom(ctx); recorder != nil {
		defer func() {
			recorder.add(MCPCall{Server: c.service, Tool: name, StartedAt: start.UTC().Format(time.RFC3339Nano)}, start, args, returned, callErr)
		}()
	}
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return errors.New("mcp_connection_failed")
	}
	var data []byte
	if result.StructuredContent != nil {
		data, err = json.Marshal(result.StructuredContent)
	} else {
		for _, v := range result.Content {
			if t, ok := v.(*mcp.TextContent); ok {
				data = []byte(t.Text)
				break
			}
		}
	}
	if len(data) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		if decoder.Decode(&returned) != nil {
			returned = string(data)
		}
	}
	if result.IsError {
		if fileToolNames[name] && len(data) > 0 && len(data) < 1024 {
			return errors.New("mcp_tool_failed: " + name + ": " + string(data))
		}
		return errors.New("mcp_tool_failed: " + name)
	}
	if err != nil || len(data) == 0 {
		return errors.New("mcp_invalid_response")
	}
	if out == nil {
		return nil
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("mcp_invalid_response")
	}
	return nil
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}
func ConnectProject(ctx context.Context, url, token string) (*MCPClient, error) {
	h := &http.Client{Transport: bearerTransport{token: token, base: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 40 * time.Second}}, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "support-crm", Version: "1.0.0"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: h, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return nil, errors.New("project_mcp_unavailable")
	}
	return &MCPClient{session: s, service: "project"}, nil
}
func NewCRM(ctx context.Context, store *Store) (*MCPClient, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "support-crm-data", Version: "1.0.0"}, nil)
	no := false
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no}
	mcp.AddTool(s, &mcp.Tool{Name: "crm_list_tickets", Description: "List support tickets. Demo tickets are labelled explicitly.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []Ticket, error) {
		v, e := store.Tickets(ctx)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_get_ticket", Description: "Get one ticket and only its conversation.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, Ticket, error) {
		v, e := store.Ticket(ctx, in.ID)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_get_user", Description: "Get the user associated with a ticket.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, User, error) {
		v, e := store.User(ctx, in.ID)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_list_users", Description: "List CRM users.", Annotations: ro}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, []User, error) {
		v, e := store.Users(ctx)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_create_user", Description: "Create a user after an explicit form submission."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Name        string         `json:"name"`
		Environment map[string]any `json:"environment,omitempty"`
	}) (*mcp.CallToolResult, User, error) {
		v, e := store.AddUser(ctx, in.Name, in.Environment)
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_create_ticket", Description: "Create a ticket from the submitted form."}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateTicketInput) (*mcp.CallToolResult, Ticket, error) {
		v, e := store.Create(ctx, Ticket{UserID: in.UserID, Title: in.Title, Description: in.Description, Category: in.Category, Priority: in.Priority, Environment: in.Environment, Context: in.Context})
		return nil, v, e
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_update_status", Description: "Set status after an explicit operator action."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}) (*mcp.CallToolResult, map[string]bool, error) {
		e := store.Status(ctx, in.ID, in.Status)
		return nil, map[string]bool{"saved": e == nil}, e
	})
	destructive := true
	mcp.AddTool(s, &mcp.Tool{Name: "crm_delete_ticket", Description: "Delete a ticket, its conversation and MCP journal after explicit operator confirmation.", Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, DeleteTicketResult, error) {
		err := store.DeleteTicket(ctx, in.ID)
		if errors.Is(err, ErrNotFound) {
			return nil, DeleteTicketResult{}, nil
		}
		return nil, DeleteTicketResult{Deleted: err == nil}, err
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crm_add_message", Description: "Append a message to one ticket."}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID      string   `json:"id"`
		Role    string   `json:"role"`
		Content string   `json:"content"`
		Sources []Source `json:"sources,omitempty"`
		Trace   *Trace   `json:"trace,omitempty"`
	}) (*mcp.CallToolResult, SavedMessage, error) {
		if in.Role != "user" && in.Role != "assistant" && in.Role != "operator" {
			return nil, SavedMessage{}, errors.New("invalid role")
		}
		limit := 30000
		if in.Role == "assistant" {
			limit = 512 << 10
		} // Includes up to four full document diffs.
		if strings.TrimSpace(in.Content) == "" || len(in.Content) > limit {
			return nil, SavedMessage{}, errors.New("invalid message")
		}
		id, e := store.AppendMessage(ctx, in.ID, in.Role, in.Content, in.Sources, in.Trace)
		return nil, SavedMessage{Saved: e == nil, MessageID: id}, e
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		return nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "support-agent", Version: "1.0.0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		ss.Close()
		return nil, err
	}
	return &MCPClient{session: cs, server: ss, service: "crm"}, nil
}
