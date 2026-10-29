package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 2048,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsClient struct {
	id       int64
	username string
	conn     *websocket.Conn
	send     chan []byte
}

type voiceEntry struct {
	channelID int64
	username  string
}

type hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
	byUser  map[int64]map[*wsClient]struct{}
	voice   map[int64]voiceEntry
}

func newHub() *hub {
	return &hub{
		clients: map[*wsClient]struct{}{},
		byUser:  map[int64]map[*wsClient]struct{}{},
		voice:   map[int64]voiceEntry{},
	}
}

func (h *hub) register(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	m, ok := h.byUser[c.id]
	if !ok {
		m = map[*wsClient]struct{}{}
		h.byUser[c.id] = m
	}
	m[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) unregister(c *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	leftVoice := false
	if m := h.byUser[c.id]; m != nil {
		delete(m, c)
		if len(m) == 0 {
			delete(h.byUser, c.id)
			if _, inVoice := h.voice[c.id]; inVoice {
				delete(h.voice, c.id)
				leftVoice = true
			}
		}
	}
	close(c.send)
	h.mu.Unlock()
	c.conn.Close()
	h.broadcastPresence()
	_ = leftVoice
}

func (h *hub) SetVoice(userID int64, username string, channelID int64) {
	h.mu.Lock()
	h.voice[userID] = voiceEntry{channelID: channelID, username: username}
	h.mu.Unlock()
	h.broadcastPresence()
}

func (h *hub) ClearVoice(userID int64) {
	h.mu.Lock()
	if _, ok := h.voice[userID]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.voice, userID)
	h.mu.Unlock()
	h.broadcastPresence()
}

type voiceMount struct {
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	ChannelID int64  `json:"channel_id"`
}

func (h *hub) voiceList() []voiceMount {
	out := make([]voiceMount, 0, len(h.voice))
	for uid, ve := range h.voice {
		out = append(out, voiceMount{UserID: uid, Username: ve.username, ChannelID: ve.channelID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

func (h *hub) broadcastPresence() {
	h.mu.Lock()
	names := map[string]struct{}{}
	for c := range h.clients {
		names[c.username] = struct{}{}
	}
	h.mu.Unlock()
	online := make([]string, 0, len(names))
	for n := range names {
		online = append(online, n)
	}
	sort.Strings(online)
	h.broadcast(map[string]any{"t": "presence", "online": online, "voice": h.voiceList()})
}

func (h *hub) broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("broadcast marshal: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default:
		}
	}
}

func (h *hub) relay(userID int64, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("relay marshal: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.byUser[userID]; m != nil {
		for c := range m {
			select {
			case c.send <- b:
			default:
			}
		}
	}
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	u := userFromCtx(r.Context())
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{id: u.ID, username: u.Username, conn: conn, send: make(chan []byte, 128)}

	go func() {
		defer s.hub.unregister(c)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var in struct {
				T         string `json:"t"`
				ChannelID int64  `json:"channel_id"`
			}
			if json.Unmarshal(data, &in) != nil {
				continue
			}
			switch in.T {
			case "ping":
				s.hub.broadcast(map[string]any{"t": "pong"})
			case "typing":
				s.hub.broadcast(map[string]any{"t": "typing", "channel_id": in.ChannelID, "username": u.Username})
			case "voice:join":
				var ctype string
				err := s.db.QueryRow(`SELECT type FROM channels WHERE id = ?`, in.ChannelID).Scan(&ctype)
				if err == nil && ctype == "voice" {
					s.hub.SetVoice(u.ID, u.Username, in.ChannelID)
				}
			case "voice:leave":
				s.hub.ClearVoice(u.ID)
			case "voice:signal":
				var sig struct {
					To   int64           `json:"to"`
					Data json.RawMessage `json:"data"`
				}
				if json.Unmarshal(data, &sig) != nil {
					continue
				}
				if sig.To > 0 && sig.To != u.ID && len(sig.Data) > 0 {
					s.hub.relay(sig.To, map[string]any{"t": "voice:signal", "from": u.ID, "data": sig.Data})
				}
			}
		}
	}()

	s.hub.register(c)
	s.hub.broadcastPresence()

	pingTick := time.NewTicker(25 * time.Second)
	defer pingTick.Stop()
	for {
		select {
		case b, ok := <-c.send:
			if !ok {
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-pingTick.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
