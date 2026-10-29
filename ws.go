package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsClient struct {
	id       int64
	username string
	conn     *websocket.Conn
	send     chan []byte
}

type hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

func newHub() *hub {
	return &hub{clients: map[*wsClient]struct{}{}}
}

func (h *hub) register(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) unregister(c *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, c)
	close(c.send)
	h.mu.Unlock()
	c.conn.Close()
	h.broadcastPresence()
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

func (h *hub) onlineList() []string {
	names := map[string]struct{}{}
	for c := range h.clients {
		names[c.username] = struct{}{}
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out
}

func (h *hub) broadcastPresence() {
	h.broadcast(map[string]any{"t": "presence", "online": h.onlineList()})
}

func (c *wsClient) write(b []byte) bool {
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(websocket.TextMessage, b) == nil
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	u := userFromCtx(r.Context())
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{id: u.ID, username: u.Username, conn: conn, send: make(chan []byte, 64)}

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
			if !c.write(b) {
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
