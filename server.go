package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"database/sql"
)

const sessionCookie = "roost_session"
const maxMessageLen = 4000

type server struct {
	db  *sql.DB
	hub *hub
}

func newServer(db *sql.DB, h *hub) (*server, error) {
	s := &server{db: db, hub: h}
	if err := s.seedDefaults(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) seedDefaults() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM channels`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		now := time.Now().Unix()
		seeds := []struct{ name, ctype string }{
			{"general", "text"},
			{"off-topic", "text"},
			{"meet", "voice"},
		}
		for _, sd := range seeds {
			if _, err := s.db.Exec(`INSERT INTO channels (name, type, position, created_at) VALUES (?, ?, ?, ?)`, sd.name, sd.ctype, now, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/setup", s.handleSetup)
	mux.HandleFunc("/api/register", s.handleRegister)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("/api/users", s.requireAuth(s.handleUsers))
	mux.HandleFunc("/api/invites", s.requireAuth(s.requireAdmin(s.handleInvites)))
	mux.HandleFunc("/api/invites/", s.requireAuth(s.requireAdmin(s.handleInviteDelete)))
	mux.HandleFunc("/api/channels", s.requireAuth(s.handleChannels))
	mux.HandleFunc("/api/channels/", s.requireAuth(s.handleChannelSub))
	mux.HandleFunc("/api/messages/", s.requireAuth(s.handleMessageUpdate))
	mux.HandleFunc("/api/ws", s.requireAuthWS(s.handleWS))

	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && strings.Contains(r.URL.Path, ".") {
			fileServer.ServeHTTP(w, r)
			return
		}
		b, err := webFiles.ReadFile("web/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userFromRequest(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		next(w, r.WithContext(withUser(r.Context(), u)))
	}
}

func (s *server) requireAuthWS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.userFromRequest(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(withUser(r.Context(), u)))
	}
}

func (s *server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFromCtx(r.Context())
		if u == nil || !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid json body")
	}
	return nil
}

func parseID(prefix string, path string) (int64, bool) {
	p := strings.TrimPrefix(path, prefix)
	var id int64
	if _, err := fmt.Sscanf(p, "%d", &id); err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

//go:embed web
var webFiles embed.FS

func sameSiteCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if o := r.Header.Get("Origin"); o != "" {
				u, err := parseOriginHost(o)
				if err != nil || !hostMatches(u, r.Host) {
					writeErr(w, http.StatusForbidden, "bad origin")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func parseOriginHost(origin string) (string, error) {
	rest := origin
	for _, pre := range []string{"http://", "https://"} {
		if strings.HasPrefix(rest, pre) {
			rest = strings.TrimPrefix(rest, pre)
			break
		}
	}
	rest = strings.Split(rest, "/")[0]
	if rest == "" {
		return "", errors.New("empty host")
	}
	if h, _, err := net.SplitHostPort(rest); err == nil {
		return h, nil
	}
	return rest, nil
}

func hostMatches(originHost, reqHost string) bool {
	h := reqHost
	if rh, _, err := net.SplitHostPort(reqHost); err == nil {
		h = rh
	}
	return strings.EqualFold(originHost, h)
}

func rateLimit(next http.Handler) http.Handler {
	strict := newLimiter(20, time.Minute)
	wide := newLimiter(600, time.Minute)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if strings.HasPrefix(r.URL.Path, "/api/login") || strings.HasPrefix(r.URL.Path, "/api/register") {
			if !strict.allow(ip) {
				writeErr(w, http.StatusTooManyRequests, "too many requests, slow down")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !wide.allow(ip) {
			writeErr(w, http.StatusTooManyRequests, "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	hits   map[string]*hit
}

type hit struct {
	count int
	start time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{window: window, max: max, hits: map[string]*hit{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h, ok := l.hits[key]
	if !ok || now.Sub(h.start) >= l.window {
		if len(l.hits) > 10000 {
			l.hits = map[string]*hit{}
		}
		l.hits[key] = &hit{count: 1, start: now}
		return true
	}
	h.count++
	return h.count <= l.max
}
