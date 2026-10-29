package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

type ctxKey int

const userKey ctxKey = 0

func withUser(ctx context.Context, u *user) context.Context {
	return context.WithValue(ctx, userKey, u)
}

func userFromCtx(ctx context.Context) *user {
	u, _ := ctx.Value(userKey).(*user)
	return u
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{3,24}$`)

func hashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), 10)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func checkPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newInviteCode() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	alphabet := "abcdefghijkmnpqrstuvwxyz23456789"
	out := make([]byte, 10)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

func (s *server) userFromRequest(r *http.Request) (*user, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, errors.New("no session")
	}
	var u user
	var expires int64
	err = s.db.QueryRow(`
		SELECT u.id, u.username, u.is_admin, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ?`, c.Value).Scan(&u.ID, &u.Username, &u.IsAdmin, &expires)
	if err != nil {
		return nil, errors.New("session not found")
	}
	if time.Now().Unix() > expires {
		return nil, errors.New("session expired")
	}
	return &u, nil
}

func (s *server) startSession(w http.ResponseWriter, userID int64) error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	exp := now + 30*24*3600
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`, token, userID, now, exp); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   30 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.EqualFold(getenv("ROOST_COOKIE_SECURE"), "1"),
	})
	return nil
}

func (s *server) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.db.Exec(`DELETE FROM sessions WHERE token = ?`, c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *server) countUsers() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.countUsers()
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	writeJSON(w, 200, map[string]bool{"needs_invite": n > 0})
}

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Invite   string `json:"invite"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !usernameRe.MatchString(body.Username) {
		writeErr(w, 400, "username must be 3-24 characters (letters, numbers, _ . -)")
		return
	}
	if len(body.Password) < 8 {
		writeErr(w, 400, "password must be at least 8 characters")
		return
	}

	n, err := s.countUsers()
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	first := n == 0

	if !first {
		invite := strings.TrimSpace(body.Invite)
		if invite == "" {
			writeErr(w, 400, "invite code required")
			return
		}
		var maxUses, uses int
		err := s.db.QueryRow(`SELECT max_uses, uses FROM invites WHERE code = ?`, invite).Scan(&maxUses, &uses)
		if err != nil {
			writeErr(w, 403, "invalid invite code")
			return
		}
		if maxUses != 0 && uses >= maxUses {
			writeErr(w, 403, "invite code already used up")
			return
		}
		if _, err := s.db.Exec(`UPDATE invites SET uses = uses + 1 WHERE code = ?`, invite); err != nil {
			writeErr(w, 500, "db error")
			return
		}
	}

	hash, err := hashPassword(body.Password)
	if err != nil {
		writeErr(w, 500, "hash error")
		return
	}
	res, err := s.db.Exec(`INSERT INTO users (username, pass_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`, body.Username, hash, boolToInt(first), time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, 409, "username already taken")
		} else {
			writeErr(w, 500, "db error")
		}
		return
	}
	id, _ := res.LastInsertId()
	if err := s.startSession(w, id); err != nil {
		writeErr(w, 500, "session error")
		return
	}
	writeJSON(w, 201, user{ID: id, Username: body.Username, IsAdmin: first})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var hash string
	var id int64
	err := s.db.QueryRow(`SELECT id, pass_hash FROM users WHERE username = ?`, body.Username).Scan(&id, &hash)
	if err != nil {
		writeErr(w, 401, "wrong username or password")
		return
	}
	ok := checkPassword(hash, body.Password)
	if !ok {
		subtle.ConstantTimeCompare([]byte("a"), []byte("b"))
		writeErr(w, 401, "wrong username or password")
		return
	}
	if err := s.startSession(w, id); err != nil {
		writeErr(w, 500, "session error")
		return
	}
	u := user{ID: id, Username: body.Username}
	s.db.QueryRow(`SELECT is_admin FROM users WHERE id = ?`, id).Scan(&u.IsAdmin)
	writeJSON(w, 200, u)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	s.endSession(w, r)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, userFromCtx(r.Context()))
}

func (s *server) handleUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id, username, is_admin FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	defer rows.Close()
	users := []user{}
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin); err == nil {
			users = append(users, u)
		}
	}
	writeJSON(w, 200, users)
}

func (s *server) handleInvites(w http.ResponseWriter, r *http.Request) {
	u := userFromCtx(r.Context())
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.Query(`
			SELECT i.code, i.max_uses, i.uses, i.created_at, u.username
			FROM invites i JOIN users u ON u.id = i.created_by
			ORDER BY i.created_at DESC`)
		if err != nil {
			writeErr(w, 500, "db error")
			return
		}
		defer rows.Close()
		type inv struct {
			Code      string `json:"code"`
			MaxUses   int    `json:"max_uses"`
			Uses      int    `json:"uses"`
			CreatedAt int64  `json:"created_at"`
			CreatedBy string `json:"created_by"`
		}
		list := []inv{}
		for rows.Next() {
			var it inv
			if err := rows.Scan(&it.Code, &it.MaxUses, &it.Uses, &it.CreatedAt, &it.CreatedBy); err == nil {
				list = append(list, it)
			}
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		var body struct {
			MaxUses int `json:"max_uses"`
		}
		_ = readJSON(w, r, &body)
		if body.MaxUses < 0 || body.MaxUses > 1000 {
			body.MaxUses = 1
		}
		code, err := newInviteCode()
		if err != nil {
			writeErr(w, 500, "code error")
			return
		}
		if _, err := s.db.Exec(`INSERT INTO invites (code, created_by, max_uses, uses, created_at) VALUES (?, ?, ?, 0, ?)`, code, u.ID, body.MaxUses, time.Now().Unix()); err != nil {
			writeErr(w, 500, "db error")
			return
		}
		writeJSON(w, 201, map[string]any{"code": code, "max_uses": body.MaxUses})
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *server) handleInviteDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, 405, "method not allowed")
		return
	}
	code, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/invites/"))
	if err != nil || code == "" {
		writeErr(w, 400, "bad invite")
		return
	}
	res, err := s.db.Exec(`DELETE FROM invites WHERE code = ?`, code)
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeErr(w, 404, "invite not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func getenv(k string) string {
	v, _ := os.LookupEnv(k)
	return v
}
