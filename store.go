package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed fixtures/tickets.json
var demoTickets []byte

type User struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Environment map[string]any `json:"environment"`
}
type Evidence struct {
	ID      string  `json:"id"`
	Source  string  `json:"source"`
	Section string  `json:"section,omitempty"`
	Text    string  `json:"text"`
	Score   float64 `json:"score,omitempty"`
	Kind    string  `json:"kind,omitempty"`
}
type Source struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Quote  string `json:"quote"`
	Kind   string `json:"kind"`
}
type Trace struct {
	startedAt       time.Time
	CRM             string             `json:"crm"`
	Project         string             `json:"project"`
	Retrieval       string             `json:"retrieval"`
	Model           string             `json:"model"`
	DurationMS      int64              `json:"durationMs"`
	Attempts        int                `json:"attempts"`
	ValidationError string             `json:"validationError,omitempty"`
	MCPCalls        []MCPCall          `json:"mcpCalls"`
	ModelContext    *ModelContextTrace `json:"modelContext,omitempty"`
}

type ContextSource struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
}
type ModelContextTrace struct {
	TicketID        string          `json:"ticketId,omitempty"`
	HistoryMessages int             `json:"historyMessages"`
	Evidence        []ContextSource `json:"evidence"`
}
type Message struct {
	ID        int64    `json:"id"`
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	CreatedAt string   `json:"createdAt"`
	Sources   []Source `json:"sources"`
	Trace     *Trace   `json:"trace,omitempty"`
}
type SavedMessage struct {
	Saved     bool  `json:"saved"`
	MessageID int64 `json:"messageId"`
}
type Ticket struct {
	ID          string         `json:"id"`
	UserID      string         `json:"userId"`
	Title       string         `json:"title"`
	Category    string         `json:"category"`
	Priority    string         `json:"priority"`
	Status      string         `json:"status"`
	Demo        bool           `json:"demo"`
	Description string         `json:"description"`
	Environment map[string]any `json:"environment"`
	Context     map[string]any `json:"context"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	Messages    []Message      `json:"messages,omitempty"`
}
type Store struct{ db *sql.DB }

var ErrNotFound = errors.New("not found")

func OpenStore(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	_, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT NOT NULL,environment TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tickets(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),title TEXT NOT NULL,category TEXT NOT NULL,priority TEXT NOT NULL,status TEXT NOT NULL,demo INTEGER NOT NULL,description TEXT NOT NULL,environment TEXT NOT NULL,context TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY AUTOINCREMENT,ticket_id TEXT NOT NULL REFERENCES tickets(id),role TEXT NOT NULL,content TEXT NOT NULL,sources TEXT NOT NULL,trace TEXT,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS messages_ticket ON messages(ticket_id,id);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0600)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func stamp() string           { return time.Now().UTC().Format(time.RFC3339Nano) }
func marshal(v any) string    { b, _ := json.Marshal(v); return string(b) }

func (s *Store) Seed(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM tickets").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	var tickets []Ticket
	if err := json.Unmarshal(demoTickets, &tickets); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO users VALUES(?,?,?)", "local-operator", "Пользователь проекта", `{"application":"ai-challenge-app"}`)
	if err != nil {
		return err
	}
	for _, t := range tickets {
		t.UserID = "local-operator"
		t.Status = "open"
		t.Demo = true
		t.CreatedAt = stamp()
		t.UpdatedAt = t.CreatedAt
		if _, err = tx.ExecContext(ctx, `INSERT INTO tickets VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.UserID, t.Title, t.Category, t.Priority, t.Status, t.Demo, t.Description, marshal(t.Environment), marshal(t.Context), t.CreatedAt, t.UpdatedAt); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO messages(ticket_id,role,content,sources,created_at) VALUES(?,?,?,?,?)`, t.ID, "user", t.Description, "[]", t.CreatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) User(ctx context.Context, id string) (User, error) {
	var u User
	var env string
	err := s.db.QueryRowContext(ctx, "SELECT id,name,environment FROM users WHERE id=?", id).Scan(&u.ID, &u.Name, &env)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	err = json.Unmarshal([]byte(env), &u.Environment)
	return u, err
}
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,name,environment FROM users ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var env string
		if err := rows.Scan(&u.ID, &u.Name, &env); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(env), &u.Environment); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) AddUser(ctx context.Context, name string, env map[string]any) (User, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 160 {
		return User{}, errors.New("Укажите имя до 160 байт")
	}
	if env == nil {
		env = map[string]any{}
	}
	u := User{ID: fmt.Sprintf("usr-%d", time.Now().UnixNano()), Name: name, Environment: env}
	_, err := s.db.ExecContext(ctx, "INSERT INTO users VALUES(?,?,?)", u.ID, u.Name, marshal(env))
	return u, err
}

type scanner interface{ Scan(...any) error }

func scanTicket(row scanner) (Ticket, error) {
	var t Ticket
	var env, ct string
	err := row.Scan(&t.ID, &t.UserID, &t.Title, &t.Category, &t.Priority, &t.Status, &t.Demo, &t.Description, &env, &ct, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if err = json.Unmarshal([]byte(env), &t.Environment); err != nil {
		return t, err
	}
	err = json.Unmarshal([]byte(ct), &t.Context)
	return t, err
}
func (s *Store) Tickets(ctx context.Context) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM tickets ORDER BY updated_at DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Ticket(ctx context.Context, id string) (Ticket, error) {
	t, err := scanTicket(s.db.QueryRowContext(ctx, "SELECT * FROM tickets WHERE id=?", id))
	if err != nil {
		return t, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,role,content,sources,trace,created_at FROM messages WHERE ticket_id=? ORDER BY id", id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	t.Messages = []Message{}
	for rows.Next() {
		var m Message
		var sources string
		var trace sql.NullString
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &sources, &trace, &m.CreatedAt); err != nil {
			return t, err
		}
		if err = json.Unmarshal([]byte(sources), &m.Sources); err != nil {
			return t, err
		}
		if trace.Valid {
			if err = json.Unmarshal([]byte(trace.String), &m.Trace); err != nil {
				return t, err
			}
		}
		t.Messages = append(t.Messages, m)
	}
	return t, rows.Err()
}
func (s *Store) Create(ctx context.Context, t Ticket) (Ticket, error) {
	t.Title = strings.TrimSpace(t.Title)
	t.Description = strings.TrimSpace(t.Description)
	if t.Title == "" || len(t.Title) > 250 || t.Description == "" || len(t.Description) > 12000 {
		return t, errors.New("Нужны тема до 250 байт и описание до 12000 байт")
	}
	if _, err := s.User(ctx, t.UserID); err != nil {
		return t, errors.New("Выберите существующего пользователя")
	}
	t.ID = fmt.Sprintf("SUP-%d", time.Now().UnixNano())
	t.Status = "open"
	t.Demo = false
	t.CreatedAt = stamp()
	t.UpdatedAt = t.CreatedAt
	if t.Category == "" {
		t.Category = "Общее"
	}
	if t.Priority != "high" {
		t.Priority = "normal"
	}
	if t.Environment == nil {
		t.Environment = map[string]any{}
	}
	if t.Context == nil {
		t.Context = map[string]any{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "INSERT INTO tickets VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", t.ID, t.UserID, t.Title, t.Category, t.Priority, t.Status, t.Demo, t.Description, marshal(t.Environment), marshal(t.Context), t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return t, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO messages(ticket_id,role,content,sources,created_at) VALUES(?,?,?,?,?)", t.ID, "user", t.Description, "[]", t.CreatedAt)
	if err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	return s.Ticket(ctx, t.ID)
}
func (s *Store) Status(ctx context.Context, id, status string) error {
	if status != "open" && status != "in_progress" && status != "resolved" {
		return errors.New("Неизвестный статус")
	}
	res, err := s.db.ExecContext(ctx, "UPDATE tickets SET status=?,updated_at=? WHERE id=?", status, stamp(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) DeleteTicket(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM messages WHERE ticket_id=?", id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM tickets WHERE id=?", id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}
func (s *Store) AddMessage(ctx context.Context, id, role, content string, sources []Source, trace *Trace) error {
	_, err := s.AppendMessage(ctx, id, role, content, sources, trace)
	return err
}
func (s *Store) AppendMessage(ctx context.Context, id, role, content string, sources []Source, trace *Trace) (int64, error) {
	if sources == nil {
		sources = []Source{}
	}
	var tr any
	if trace != nil {
		tr = marshal(trace)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "INSERT INTO messages(ticket_id,role,content,sources,trace,created_at) VALUES(?,?,?,?,?,?)", id, role, content, marshal(sources), tr, stamp())
	if err != nil {
		return 0, err
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE tickets SET updated_at=? WHERE id=?", stamp(), id)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return messageID, nil
}

// Diagnostic metadata is finalized after the last MCP call, so its own result is
// included without recursively invoking another tool to save the journal.
func (s *Store) UpdateMessageTrace(ctx context.Context, messageID int64, trace Trace) error {
	result, err := s.db.ExecContext(ctx, "UPDATE messages SET trace=? WHERE id=? AND role='assistant'", marshal(trace), messageID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
