package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type Config struct {
	Addr, DB, AccessToken, Key, Model, ProjectURL, ProjectToken string
	Seed, SecureCookies                                         bool
}

func loadEnv(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		if os.Getenv(k) == "" {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func config() (Config, error) {
	c := Config{Addr: env("SUPPORT_ADDR", "127.0.0.1:8090"), DB: env("SUPPORT_DB", ".local/support.db"), AccessToken: os.Getenv("SUPPORT_ACCESS_TOKEN"), Key: os.Getenv("DEEPSEEK_API_KEY"), Model: env("DEEPSEEK_MODEL", "deepseek-flash"), ProjectURL: env("PROJECT_MCP_URL", "http://127.0.0.1:8091/mcp"), ProjectToken: os.Getenv("PROJECT_MCP_TOKEN"), Seed: env("SUPPORT_SEED_DEMO", "false") == "true", SecureCookies: env("SUPPORT_SECURE_COOKIES", "false") == "true"}
	if len(c.AccessToken) < 32 || len(c.ProjectToken) < 32 {
		return c, errors.New("set SUPPORT_ACCESS_TOKEN and PROJECT_MCP_TOKEN to at least 32 random characters")
	}
	u, e := url.Parse(c.ProjectURL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return c, errors.New("PROJECT_MCP_URL must be an HTTP(S) URL without credentials")
	}
	return c, nil
}

type App struct {
	Config    Config
	CRM       Caller
	Assistant *Assistant
	turns     sync.Map
	general   chan struct{}
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if !decode(w, r, &in) {
			return
		}
		if subtle.ConstantTimeCompare([]byte(in.Token), []byte(a.Config.AccessToken)) != 1 {
			respond(w, 401, map[string]string{"error": "Неверный токен доступа"})
			return
		}
		hash := sha256.Sum256([]byte("support-session:" + a.Config.AccessToken))
		http.SetCookie(w, &http.Cookie{Name: "support_session", Value: hex.EncodeToString(hash[:]), Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "support_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies || r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	api := http.NewServeMux()
	api.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		status := map[string]any{"deepseekConfigured": a.Config.Key != "", "model": a.Config.Model, "project": "unavailable", "crm": "mcp/sqlite", "serverOS": runtime.GOOS}
		client, err := ConnectProject(ctx, a.Config.ProjectURL, a.Config.ProjectToken)
		if err == nil {
			defer client.Close()
			var info map[string]any
			if client.Call(ctx, "project_get_info", map[string]any{}, &info) == nil {
				status["project"] = "connected"
				status["info"] = info
			}
		}
		respond(w, 200, status)
	})
	api.HandleFunc("GET /api/tickets", func(w http.ResponseWriter, r *http.Request) { a.call(w, r, "crm_list_tickets", map[string]any{}) })
	api.HandleFunc("GET /api/tickets/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.call(w, r, "crm_get_ticket", map[string]string{"id": r.PathValue("id")})
	})
	api.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) { a.call(w, r, "crm_list_users", map[string]any{}) })
	api.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name        string         `json:"name"`
			Environment map[string]any `json:"environment,omitempty"`
		}
		if !decode(w, r, &in) {
			return
		}
		a.call(w, r, "crm_create_user", in)
	})
	api.HandleFunc("POST /api/tickets", func(w http.ResponseWriter, r *http.Request) {
		var in CreateTicketInput
		if !decode(w, r, &in) {
			return
		}
		a.call(w, r, "crm_create_ticket", in)
	})
	api.HandleFunc("PATCH /api/tickets/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Status string `json:"status"`
		}
		if !decode(w, r, &in) {
			return
		}
		a.call(w, r, "crm_update_status", map[string]string{"id": r.PathValue("id"), "status": in.Status})
	})
	api.HandleFunc("DELETE /api/tickets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		gate := a.ticketGate(id)
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			respond(w, 409, map[string]string{"error": "Дождитесь завершения ответа, затем удалите обращение"})
			return
		}
		var result DeleteTicketResult
		if err := a.CRM.Call(r.Context(), "crm_delete_ticket", map[string]string{"id": id}, &result); err != nil {
			respond(w, 500, map[string]string{"error": "Не удалось удалить обращение. Повторите попытку"})
			return
		}
		if !result.Deleted {
			respond(w, 404, map[string]string{"error": "Обращение уже удалено или не найдено"})
			return
		}
		respond(w, 200, result)
	})
	api.HandleFunc("POST /api/tickets/{id}/notes", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Content string `json:"content"`
		}
		if !decode(w, r, &in) {
			return
		}
		a.call(w, r, "crm_add_message", map[string]string{"id": r.PathValue("id"), "role": "operator", "content": in.Content})
	})
	api.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TicketID string `json:"ticketId"`
			Question string `json:"question"`
		}
		if !decode(w, r, &in) {
			return
		}
		key := in.TicketID
		if key == "" {
			key = "__general"
		}
		gate := a.ticketGate(key)
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			respond(w, 409, map[string]string{"error": "Ответ по этому обращению уже готовится"})
			return
		}
		select {
		case a.general <- struct{}{}:
			defer func() { <-a.general }()
		default:
			respond(w, 429, map[string]string{"error": "Сервис занят. Повторите запрос позже"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
		defer cancel()
		reply, err := a.Assistant.Ask(ctx, in.TicketID, in.Question)
		if err != nil {
			respond(w, 400, map[string]any{"error": err.Error(), "trace": reply.Trace})
			return
		}
		respond(w, 200, reply)
	})
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(r) {
			respond(w, 401, map[string]string{"error": "Нужен токен доступа"})
			return
		}
		api.ServeHTTP(w, r)
	}))
	files, _ := fs.Sub(assets, "web")
	mux.Handle("/", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					respond(w, 403, map[string]string{"error": "Недопустимый источник запроса"})
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) ticketGate(id string) chan struct{} {
	raw, _ := a.turns.LoadOrStore(id, make(chan struct{}, 1))
	return raw.(chan struct{})
}
func (a *App) authorized(r *http.Request) bool {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(a.Config.AccessToken)) == 1
	}
	cookie, err := r.Cookie("support_session")
	if err != nil {
		return false
	}
	hash := sha256.Sum256([]byte("support-session:" + a.Config.AccessToken))
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(hex.EncodeToString(hash[:]))) == 1
}
func (a *App) call(w http.ResponseWriter, r *http.Request, name string, args any) {
	var out any
	if err := a.CRM.Call(r.Context(), name, args, &out); err != nil {
		respond(w, 400, map[string]string{"error": "Операция CRM не выполнена. Проверьте данные и ID обращения"})
		return
	}
	respond(w, 200, out)
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		respond(w, 400, map[string]string{"error": "Некорректный JSON или слишком большой запрос"})
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		respond(w, 400, map[string]string{"error": "Ожидается один JSON-объект"})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func main() {
	if err := loadEnv(".env"); err != nil {
		log.Fatal("cannot read .env")
	}
	cfg, err := config()
	if err != nil {
		log.Fatal(err)
	}
	store, err := OpenStore(cfg.DB)
	if err != nil {
		log.Fatal("cannot open CRM database")
	}
	defer store.Close()
	logFile, logErr := os.OpenFile(filepath.Join(filepath.Dir(cfg.DB), "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if logErr == nil {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	}
	if cfg.Seed {
		if err = store.Seed(context.Background()); err != nil {
			log.Fatal("cannot initialize demo tickets")
		}
	}
	crm, err := NewCRM(context.Background(), store)
	if err != nil {
		log.Fatal("cannot connect CRM MCP")
	}
	defer crm.Close()
	assistant := &Assistant{CRM: crm, Model: NewDeepSeek(cfg.Key, cfg.Model), ModelName: cfg.Model, Secrets: []string{cfg.Key, cfg.AccessToken, cfg.ProjectToken}, SaveTrace: store.UpdateMessageTrace, Project: func(ctx context.Context) (Caller, func(), error) {
		c, err := ConnectProject(ctx, cfg.ProjectURL, cfg.ProjectToken)
		if err != nil {
			return nil, func() {}, err
		}
		return c, c.Close, nil
	}}
	app := &App{Config: cfg, CRM: crm, Assistant: assistant, general: make(chan struct{}, 4)}
	server := &http.Server{Addr: cfg.Addr, Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	stop := make(chan os.Signal, 1)
	shutdownDone := make(chan struct{})
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		defer close(shutdownDone)
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Support CRM listening on %s · provider DeepSeek · model %s", cfg.Addr, cfg.Model)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-shutdownDone
}
