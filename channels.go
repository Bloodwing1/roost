package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type channel struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Created int64  `json:"created_at"`
}

type message struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channel_id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Content   string `json:"content"`
	Created   int64  `json:"created_at"`
	EditedAt  *int64 `json:"edited_at"`
	IsAdmin   bool   `json:"is_admin"`
}

func (s *server) handleChannels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rows, err := s.db.Query(`SELECT id, name, type, created_at FROM channels ORDER BY position, id`)
		if err != nil {
			writeErr(w, 500, "db error")
			return
		}
		defer rows.Close()
		list := []channel{}
		for rows.Next() {
			var c channel
			if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Created); err == nil {
				list = append(list, c)
			}
		}
		writeJSON(w, 200, list)
	case http.MethodPost:
		u := userFromCtx(r.Context())
		if !u.IsAdmin {
			writeErr(w, 403, "admin only")
			return
		}
		var body struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := readJSON(w, r, &body); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if body.Type == "" {
			body.Type = "text"
		}
		if body.Type != "text" && body.Type != "voice" {
			writeErr(w, 400, "channel type must be text or voice")
			return
		}
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if err := validateChannelName(name); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		now := time.Now().Unix()
		res, err := s.db.Exec(`INSERT INTO channels (name, type, position, created_at) VALUES (?, ?, ?, ?)`, name, body.Type, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeErr(w, 409, "channel already exists")
				return
			}
			writeErr(w, 500, "db error")
			return
		}
		id, _ := res.LastInsertId()
		c := channel{ID: id, Name: name, Type: body.Type, Created: now}
		s.hub.broadcast(map[string]any{"t": "channel:new", "channel": c})
		writeJSON(w, 201, c)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *server) handleChannelSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/channels/")
	parts := strings.SplitN(rest, "/", 2)
	id, ok := parseID("", parts[0])
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.handleChannelGet(w, r, id)
		case http.MethodDelete:
			s.handleChannelDelete(w, r, id)
		default:
			writeErr(w, 405, "method not allowed")
		}
		return
	}
	switch parts[1] {
	case "messages":
		switch r.Method {
		case http.MethodGet:
			s.handleMessages(w, r, id)
		case http.MethodPost:
			s.handleChannelPostMessage(w, r, id)
		default:
			writeErr(w, 405, "method not allowed")
		}
	default:
		writeErr(w, 404, "not found")
	}
}

func (s *server) handleChannelGet(w http.ResponseWriter, r *http.Request, id int64) {
	var c channel
	err := s.db.QueryRow(`SELECT id, name, type, created_at FROM channels WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Type, &c.Created)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "channel not found")
		return
	} else if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	writeJSON(w, 200, c)
}

func (s *server) handleChannelDelete(w http.ResponseWriter, r *http.Request, id int64) {
	u := userFromCtx(r.Context())
	if !u.IsAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	res, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, 404, "channel not found")
		return
	}
	s.hub.broadcast(map[string]any{"t": "channel:delete", "id": id})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request, channelID int64) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	before := int64(9223372036854775807)
	if b := strings.TrimSpace(r.URL.Query().Get("before")); b != "" {
		var v int64
		if _, err := strconv.ParseInt(b, 10, 64); err != nil {
			writeErr(w, 400, "bad before param")
			return
		}
		before = v
	}
	limit := 50
	rows, err := s.db.Query(`
		SELECT m.id, m.channel_id, m.user_id, u.username, m.content, m.created_at, m.edited_at, u.is_admin
		FROM messages m JOIN users u ON u.id = m.user_id
		WHERE m.channel_id = ? AND m.id < ?
		ORDER BY m.id DESC LIMIT ?`, channelID, before, limit+1)
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	defer rows.Close()
	msgs := []message{}
	for rows.Next() {
		var m message
		m.IsAdmin = false
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Username, &m.Content, &m.Created, &m.EditedAt, &m.IsAdmin); err == nil {
			msgs = append(msgs, m)
		}
	}
	hasMore := len(msgs) > limit
	if hasMore {
		msgs = msgs[:limit]
	}
	writeJSON(w, 200, map[string]any{"messages": msgs, "has_more": hasMore})
}

func (s *server) handleChannelPostMessage(w http.ResponseWriter, r *http.Request, channelID int64) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	u := userFromCtx(r.Context())
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeErr(w, 400, "empty message")
		return
	}
	if len([]rune(content)) > maxMessageLen {
		writeErr(w, 400, "message too long")
		return
	}
	var ctype string
	if err := s.db.QueryRow(`SELECT type FROM channels WHERE id = ?`, channelID).Scan(&ctype); err != nil {
		writeErr(w, 404, "channel not found")
		return
	}
	if ctype == "voice" {
		writeErr(w, 400, "voice channels have no messages")
		return
	}
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO messages (channel_id, user_id, content, created_at) VALUES (?, ?, ?, ?)`, channelID, u.ID, content, now)
	if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	id, _ := res.LastInsertId()
	m := message{ID: id, ChannelID: channelID, UserID: u.ID, Username: u.Username, Content: content, Created: now, IsAdmin: u.IsAdmin}
	s.hub.broadcast(map[string]any{"t": "message:new", "message": m})
	writeJSON(w, 201, m)
}

func (s *server) handleMessageUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID("/api/messages/", r.URL.Path)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		s.handleMessageEdit(w, r, id)
	case http.MethodDelete:
		s.handleMessageDelete(w, r, id)
	default:
		writeErr(w, 405, "method not allowed")
	}
}

func (s *server) handleMessageEdit(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Content string `json:"content"`
	}
	if err := readJSON(w, r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	content := strings.TrimSpace(body.Content)
	if content == "" {
		writeErr(w, 400, "empty message")
		return
	}
	if len([]rune(content)) > maxMessageLen {
		writeErr(w, 400, "message too long")
		return
	}
	now := time.Now().Unix()
	var channelID, userID int64
	err := s.db.QueryRow(`SELECT channel_id, user_id FROM messages WHERE id = ?`, id).Scan(&channelID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "message not found")
		return
	} else if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	u := userFromCtx(r.Context())
	if u.ID != userID {
		writeErr(w, 403, "you can only edit your own messages")
		return
	}
	if _, err := s.db.Exec(`UPDATE messages SET content = ?, edited_at = ? WHERE id = ?`, content, now, id); err != nil {
		writeErr(w, 500, "db error")
		return
	}
	var isAdmin bool
	s.db.QueryRow(`SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&isAdmin)
	m := message{ID: id, ChannelID: channelID, UserID: userID, Username: u.Username, Content: content, EditedAt: &now, IsAdmin: isAdmin}
	s.hub.broadcast(map[string]any{"t": "message:edit", "message": m})
	writeJSON(w, 200, m)
}

func (s *server) handleMessageDelete(w http.ResponseWriter, r *http.Request, id int64) {
	var channelID, userID int64
	err := s.db.QueryRow(`SELECT channel_id, user_id FROM messages WHERE id = ?`, id).Scan(&channelID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "message not found")
		return
	} else if err != nil {
		writeErr(w, 500, "db error")
		return
	}
	u := userFromCtx(r.Context())
	if u.ID != userID && !u.IsAdmin {
		writeErr(w, 403, "you cannot delete this message")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM messages WHERE id = ?`, id); err != nil {
		writeErr(w, 500, "db error")
		return
	}
	s.hub.broadcast(map[string]any{"t": "message:delete", "id": id, "channel_id": channelID})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func validateChannelName(name string) error {
	if len(name) < 2 || len(name) > 32 {
		return errors.New("channel name must be 2-32 characters")
	}
	for _, c := range name {
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
		if !ok {
			return errors.New("channel name may only contain a-z, 0-9 and -")
		}
	}
	return nil
}
