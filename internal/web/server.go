// Package web serves the embedded UI and the JSON/SSE API on loopback.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed ui
var uiFiles embed.FS

const cookieName = "ssa_session"

// Server is the HTTP side of the daemon. State and Quit are provided by the app.
type Server struct {
	AppID   string
	Version string
	Port    int
	Token   string
	Icon    string
	Log     *log.Logger
	State   func() State
	Quit    func()
	// Action handles UI buttons; returns a user-facing message.
	Action func(kind, gameID string) (string, error)

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// Notify pushes the current state to all open windows.
func (s *Server) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ping", s.ping)
	mux.HandleFunc("GET /icon.svg", s.icon)
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /assets/", s.auth(files))
	mux.Handle("GET /manifest.webmanifest", s.auth(files))
	mux.Handle("GET /api/state", s.auth(http.HandlerFunc(s.state)))
	mux.Handle("GET /api/events", s.auth(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/action", s.auth(http.HandlerFunc(s.action)))
	mux.Handle("POST /api/quit", s.auth(http.HandlerFunc(s.quit)))
	return s.checkHost(mux)
}

// checkHost rejects requests whose Host is not our loopback address, which
// blocks DNS-rebinding attacks from web pages.
func (s *Server) checkHost(next http.Handler) http.Handler {
	allowed := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.Port): true,
		fmt.Sprintf("localhost:%d", s.Port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	token := r.Header.Get("X-SaveSync-Token")
	if c, err := r.Cookie(cookieName); err == nil && token == "" {
		token = c.Value
	}
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.Token)) == 1
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// index exchanges the one-time ?t= token for a cookie, then serves the app.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get("t"); t != "" {
		if subtle.ConstantTimeCompare([]byte(t), []byte(s.Token)) == 1 {
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    s.Token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   365 * 24 * 3600,
			})
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>SaveSync Ally</title>
<body style="font-family:system-ui;background:#0f1115;color:#e8eaf0;display:grid;place-items:center;height:100vh;margin:0">
<p>Otevři SaveSync Ally přes zástupce aplikace.</p></body>`)
		return
	}
	data, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) ping(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"app": s.AppID, "version": s.Version})
}

func (s *Server) icon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	fmt.Fprint(w, s.Icon)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.State())
}

// events streams the full state whenever it changes (Server-Sent Events).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan struct{}]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	send := func() bool {
		data, err := json.Marshal(s.State())
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send() {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		GameID string `json:"gameId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Neplatný požadavek."})
		return
	}
	msg, err := s.Action(req.Kind, req.GameID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Server) quit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"message": "stopping"})
	go s.Quit()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
