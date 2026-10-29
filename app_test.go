package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type testSrv struct {
	url   string
	close func()
}

func newTestServer(t *testing.T) *testSrv {
	t.Helper()
	db, err := makeTestDB(t)
	if err != nil {
		t.Fatalf("makeTestDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	srv, err := newServer(db, newHub())
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	return newTestSrvFromHandler(t, mux)
}

type testClient struct {
	base string
	http *http.Client
}

func newClient(base string) *testClient {
	jar, _ := cookiejar.New(nil)
	return &testClient{base: base, http: &http.Client{Jar: jar}}
}

func (c *testClient) doJSON(method, path string, body any) (int, map[string]any) {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, nil
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	var out map[string]any
	if b, rerr := io.ReadAll(res.Body); rerr == nil && len(b) > 0 {
		json.Unmarshal(b, &out)
	}
	return res.StatusCode, out
}

func (c *testClient) post(path string, body any) (int, map[string]any) {
	return c.doJSON(http.MethodPost, path, body)
}

func (c *testClient) get(path string) (int, map[string]any) {
	return c.doJSON(http.MethodGet, path, nil)
}

func (c *testClient) patch(path string, body any) (int, map[string]any) {
	return c.doJSON(http.MethodPatch, path, body)
}

func (c *testClient) del(path string) (int, map[string]any) {
	return c.doJSON(http.MethodDelete, path, nil)
}

func (c *testClient) ws(path string) (*websocket.Conn, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	hdr := http.Header{}
	for _, ck := range c.http.Jar.Cookies(u) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+u.Host+path, hdr)
	return conn, err
}

func (s *testSrv) closeIt() {
	if s.close != nil {
		s.close()
	}
}

func expectStatus(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status = %d, want %d", what, got, want)
	}
}

func invFor(t *testing.T, c *testClient, uses int) string {
	t.Helper()
	_, inv := c.post("/api/invites", map[string]int{"max_uses": uses})
	code, _ := inv["code"].(string)
	if code == "" {
		t.Fatal("invite code empty")
	}
	return code
}

func intStr(n int) string {
	return fmt.Sprint(n)
}

func toStrList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, s := range arr {
		out = append(out, s.(string))
	}
	return out
}

func TestAuthFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.closeIt()

	admin := newClient(ts.url)

	st, body := admin.get("/api/setup")
	expectStatus(t, st, 200, "setup")
	if body["needs_invite"] == true {
		t.Fatalf("first registration should not need invite")
	}

	st, adminMe := admin.post("/api/register", map[string]string{"username": "admin", "password": "password1"})
	expectStatus(t, st, 201, "register admin")
	if adminMe["is_admin"] != true {
		t.Fatalf("first user should be admin")
	}

	st, _ = admin.get("/api/me")
	expectStatus(t, st, 200, "me")

	st, _ = admin.post("/api/login", map[string]string{"username": "admin", "password": "wrong"})
	expectStatus(t, st, 401, "wrong password")

	bob := newClient(ts.url)
	st, _ = bob.post("/api/register", map[string]string{"username": "bob", "password": "password1"})
	expectStatus(t, st, 400, "register without invite")

	st, _ = bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": "not-real-code"})
	expectStatus(t, st, 403, "register with bad invite")

	st, _ = bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": "aaaaaaaaaa"})
	expectStatus(t, st, 403, "register with unknown real-format invite")

	st, _ = bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": invFor(t, admin, 1)})
	expectStatus(t, st, 201, "register with fresh invite")

	bob2 := newClient(ts.url)
	code2 := invFor(t, admin, 1)
	st, _ = bob2.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": code2})
	expectStatus(t, st, 409, "username taken consumes invite")

	carol := newClient(ts.url)
	st, _ = carol.post("/api/register", map[string]string{"username": "carol", "password": "password1", "invite": code2})
	expectStatus(t, st, 403, "single-use invite exhausted")

	dave := newClient(ts.url)
	st, _ = dave.post("/api/register", map[string]string{"username": "dave", "password": "short"})
	expectStatus(t, st, 400, "short password")

	st, _ = dave.post("/api/register", map[string]string{"username": "x!", "password": "password1", "invite": invFor(t, admin, 1)})
	expectStatus(t, st, 400, "bad username")

	dave2 := newClient(ts.url)
	st, _ = dave2.post("/api/register", map[string]string{"username": "dave", "password": "password1", "invite": invFor(t, admin, 1)})
	expectStatus(t, st, 201, "register dave")

	st, _ = dave2.post("/api/login", map[string]string{"username": "dave", "password": "wrong-password"})
	expectStatus(t, st, 401, "login wrong password")

	st, _ = dave2.post("/api/login", map[string]string{"username": "dave", "password": "password1"})
	expectStatus(t, st, 200, "login ok")

	st, _ = dave2.get("/api/invites")
	expectStatus(t, st, 403, "non-admin list invites")

	code := invFor(t, admin, 1)
	st, _ = dave2.del("/api/invites/" + code)
	expectStatus(t, st, 403, "non-admin delete invite")
	st, _ = admin.del("/api/invites/" + code)
	expectStatus(t, st, 200, "admin delete invite")
	st, _ = admin.del("/api/invites/" + code)
	expectStatus(t, st, 404, "delete missing invite")
}

func TestChannelsAndMessages(t *testing.T) {
	ts := newTestServer(t)
	defer ts.closeIt()

	admin := newClient(ts.url)
	admin.post("/api/register", map[string]string{"username": "admin", "password": "password1"})

	bob := newClient(ts.url)
	bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": invFor(t, admin, 5)})

	st, ch := admin.post("/api/channels", map[string]string{"name": "projects"})
	expectStatus(t, st, 201, "create channel")
	chID := int(ch["id"].(float64))

	st, _ = admin.post("/api/channels", map[string]string{"name": "projects"})
	expectStatus(t, st, 409, "duplicate channel")

	st, _ = admin.post("/api/channels", map[string]string{"name": "Bad Name!"})
	expectStatus(t, st, 400, "bad channel name")

	st, _ = bob.post("/api/channels", map[string]string{"name": "nope"})
	expectStatus(t, st, 403, "non-admin create channel")

	st, m1 := bob.post("/api/channels/"+intStr(chID)+"/messages", map[string]string{"content": "hello roost"})
	expectStatus(t, st, 201, "post message")
	msgID := int(m1["id"].(float64))

	st, _ = bob.post("/api/channels/"+intStr(chID)+"/messages", map[string]string{"content": ""})
	expectStatus(t, st, 400, "empty message")

	st, list := bob.get("/api/channels/" + intStr(chID) + "/messages")
	messages := list["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	st, _ = bob.patch("/api/messages/"+intStr(msgID), map[string]string{"content": "hello edited"})
	expectStatus(t, st, 200, "edit own message")

	st, _ = bob.patch("/api/messages/"+intStr(msgID), map[string]string{"content": ""})
	expectStatus(t, st, 400, "edit to empty")

	carol := newClient(ts.url)
	carol.post("/api/register", map[string]string{"username": "carol", "password": "password1", "invite": invFor(t, admin, 5)})
	st, cm := carol.post("/api/channels/"+intStr(chID)+"/messages", map[string]string{"content": "carol was here"})
	expectStatus(t, st, 201, "carol posts")
	carolMsgID := int(cm["id"].(float64))

	st, _ = bob.patch("/api/messages/"+intStr(carolMsgID), map[string]string{"content": "hijack"})
	expectStatus(t, st, 403, "edit other user's message")

	st, _ = bob.del("/api/messages/" + intStr(carolMsgID))
	expectStatus(t, st, 403, "delete other user's message as non-admin")

	st, _ = admin.del("/api/messages/" + intStr(carolMsgID))
	expectStatus(t, st, 200, "admin delete message")

	st, _ = bob.del("/api/messages/" + intStr(msgID))
	expectStatus(t, st, 200, "delete own message")

	st, _ = bob.del("/api/channels/" + intStr(chID))
	expectStatus(t, st, 403, "non-admin delete channel")

	st, _ = admin.del("/api/channels/" + intStr(chID))
	expectStatus(t, st, 200, "admin delete channel")

	st, listAfter := bob.get("/api/channels/" + intStr(chID) + "/messages")
	expectStatus(t, st, 200, "messages of deleted channel")
	if msgs := listAfter["messages"].([]any); len(msgs) != 0 {
		t.Fatalf("cascade delete failed, %d messages left for deleted channel", len(msgs))
	}
}

func TestWSDeliverAndPresence(t *testing.T) {
	ts := newTestServer(t)
	defer ts.closeIt()

	admin := newClient(ts.url)
	admin.post("/api/register", map[string]string{"username": "admin", "password": "password1"})
	_, ch := admin.post("/api/channels", map[string]string{"name": "lounge"})
	chID := int(ch["id"].(float64))

	bob := newClient(ts.url)
	bob.post("/api/register", map[string]string{"username": "bob", "password": "password1", "invite": invFor(t, admin, 5)})

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

	adminWS.SetReadDeadline(time.Now().Add(5 * time.Second))
	bobWS.SetReadDeadline(time.Now().Add(5 * time.Second))

	sawAdmin, sawBob := false, false
	for i := 0; i < 8; i++ {
		_, data, err := bobWS.ReadMessage()
		if err != nil {
			t.Fatalf("bob read: %v", err)
		}
		var ev map[string]any
		json.Unmarshal(data, &ev)
		if ev["t"] == "presence" {
			for _, n := range toStrList(ev["online"]) {
				if n == "admin" {
					sawAdmin = true
				}
				if n == "bob" {
					sawBob = true
				}
			}
			if sawAdmin && sawBob {
				break
			}
		}
	}
	if !sawAdmin || !sawBob {
		t.Fatalf("presence incomplete: admin=%v bob=%v", sawAdmin, sawBob)
	}

	st, _ := admin.post("/api/channels/"+intStr(chID)+"/messages", map[string]string{"content": "ws test"})
	expectStatus(t, st, 201, "post message")

	gotMsg := false
	for i := 0; i < 8 && !gotMsg; i++ {
		_, data, err := bobWS.ReadMessage()
		if err != nil {
			t.Fatalf("bob read2: %v", err)
		}
		var ev map[string]any
		json.Unmarshal(data, &ev)
		if ev["t"] == "message:new" {
			m := ev["message"].(map[string]any)
			if m["content"] == "ws test" {
				if int(m["channel_id"].(float64)) != chID {
					t.Fatalf("wrong channel id")
				}
				gotMsg = true
			}
		}
	}
	if !gotMsg {
		t.Fatalf("message:new not delivered")
	}

	bobWS.WriteMessage(websocket.TextMessage, []byte(`{"t":"typing","channel_id":`+intStr(chID)+`}`))
	gotTyping := false
	for i := 0; i < 8 && !gotTyping; i++ {
		_, data, err := adminWS.ReadMessage()
		if err != nil {
			t.Fatalf("admin read: %v", err)
		}
		var ev map[string]any
		json.Unmarshal(data, &ev)
		if ev["t"] == "typing" && ev["username"] == "bob" {
			gotTyping = true
		}
	}
	if !gotTyping {
		t.Fatalf("typing not delivered to admin")
	}
}
