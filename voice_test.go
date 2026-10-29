package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func readEvent(t *testing.T, ws *websocket.Conn) map[string]any {
	t.Helper()
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("ws unmarshal: %v", err)
	}
	return ev
}

func readUntil(t *testing.T, ws *websocket.Conn, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for i := 0; i < 64; i++ {
		ev := readEvent(t, ws)
		if match(ev) {
			return ev
		}
	}
	t.Fatal("event never observed")
	return nil
}

func voiceOf(t *testing.T, ev map[string]any) []map[string]any {
	t.Helper()
	raw, ok := ev["voice"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func voiceHas(list []map[string]any, username string, chID int) bool {
	for _, v := range list {
		if v["username"] == username && int(v["channel_id"].(float64)) == chID {
			return true
		}
	}
	return false
}

func (c *testClient) getAny(path string) (int, []any) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, bytes.NewReader(nil))
	if err != nil {
		return 0, nil
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	if b, rerr := io.ReadAll(res.Body); rerr == nil && len(b) > 0 {
		var arr []any
		if json.Unmarshal(b, &arr) == nil {
			return res.StatusCode, arr
		}
	}
	return res.StatusCode, nil
}

func rawChannels(t *testing.T, c *testClient) []map[string]any {
	t.Helper()
	_, arr := c.getAny("/api/channels")
	out := make([]map[string]any, 0, len(arr))
	for _, r := range arr {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func newVoiceSetup(t *testing.T) (srv *testSrv, admin, bob *testClient, chID, adminID, bobID int) {
	srv = newTestServer(t)
	admin = newClient(srv.url)
	st, me := admin.post("/api/register", map[string]string{"username": "admin", "password": "password1"})
	if st != 201 {
		t.Fatalf("admin register: %d", st)
	}
	adminID = int(me["id"].(float64))

	st, ch := admin.post("/api/channels", map[string]string{"name": "hangout", "type": "voice"})
	if st != 201 {
		t.Fatalf("create voice channel: %d", st)
	}
	chID = int(ch["id"].(float64))

	bob = newClient(srv.url)
	st, me2 := bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": invFor(t, admin, 5)})
	if st != 201 {
		t.Fatalf("bob register: %d", st)
	}
	bobID = int(me2["id"].(float64))
	return
}

func TestVoiceChannelTypes(t *testing.T) {
	ts, admin, _, _, _, _ := newVoiceSetup(t)
	defer ts.closeIt()

	channelList := rawChannels(t, admin)
	foundVoice, foundText := false, false
	for _, c := range channelList {
		name := c["name"].(string)
		switch name {
		case "hangout":
			if c["type"] != "voice" {
				t.Fatalf("hangout type = %v", c["type"])
			}
			foundVoice = true
		case "general":
			if c["type"] != "text" {
				t.Fatalf("general type = %v", c["type"])
			}
			foundText = true
		}
	}
	if !foundVoice || !foundText {
		t.Fatalf("channels missing voice=%v text=%v (%d channels)", foundVoice, foundText, len(channelList))
	}

	st, _ := admin.post("/api/channels", map[string]string{"name": "weird", "type": "warp"})
	expectStatus(t, st, 400, "bad channel type")

	chID := 0
	for _, c := range channelList {
		if c["name"] == "hangout" {
			chID = int(c["id"].(float64))
		}
	}
	st, _ = admin.post("/api/channels/"+intStr(chID)+"/messages", map[string]string{"content": "hi"})
	expectStatus(t, st, 400, "message in voice channel")
}

func TestVoiceJoinSignalLeave(t *testing.T) {
	ts, admin, bob, chID, adminID, bobID := newVoiceSetup(t)
	defer ts.closeIt()

	adminWS, err := admin.ws("/api/ws")
	if err != nil {
		t.Fatalf("admin ws: %v", err)
	}
	defer adminWS.Close()
	bobWS, err := bob.ws("/api/ws")
	if err != nil {
		t.Fatalf("bob ws: %v", err)
	}
	defer bobWS.Close()
	adminWS.SetReadDeadline(time.Now().Add(10 * time.Second))
	bobWS.SetReadDeadline(time.Now().Add(10 * time.Second))

	bobWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"voice:join","channel_id":`+intStr(chID)+`}`))
	readUntil(t, adminWS, func(ev map[string]any) bool {
		if ev["t"] != "presence" {
			return false
		}
		return voiceHas(voiceOf(t, ev), "bob", chID)
	})

	adminWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"voice:join","channel_id":`+intStr(chID)+`}`))
	readUntil(t, adminWS, func(ev map[string]any) bool {
		if ev["t"] != "presence" {
			return false
		}
		return voiceHas(voiceOf(t, ev), "admin", chID) && voiceHas(voiceOf(t, ev), "bob", chID)
	})

	bobWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"voice:signal","to":`+intStr(adminID)+`,"data":{"type":"offer","sdp":"fake-sdp"}}`))
	readUntil(t, adminWS, func(ev map[string]any) bool {
		if ev["t"] != "voice:signal" {
			return false
		}
		if int(ev["from"].(float64)) != bobID {
			t.Fatalf("signal from wrong user: %v", ev["from"])
		}
		data, ok := ev["data"].(map[string]any)
		return ok && data["type"] == "offer" && data["sdp"] == "fake-sdp"
	})

	adminWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"voice:signal","to":`+intStr(bobID)+`,"data":{"type":"answer","sdp":"fake-answer"}}`))
	readUntil(t, bobWS, func(ev map[string]any) bool {
		if ev["t"] != "voice:signal" {
			return false
		}
		if int(ev["from"].(float64)) != adminID {
			t.Fatalf("relay from wrong user")
		}
		data := ev["data"].(map[string]any)
		return data["type"] == "answer" && data["sdp"] == "fake-answer"
	})

	bobWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"voice:leave"}`))
	readUntil(t, adminWS, func(ev map[string]any) bool {
		if ev["t"] != "presence" {
			return false
		}
		vs := voiceOf(t, ev)
		return !voiceHas(vs, "bob", chID) && voiceHas(vs, "admin", chID)
	})

	bobWS.Close()
	readUntil(t, adminWS, func(ev map[string]any) bool {
		if ev["t"] != "presence" {
			return false
		}
		return !voiceHas(voiceOf(t, ev), "bob", chID)
	})
}
