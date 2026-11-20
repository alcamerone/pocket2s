package rooms

import (
	"bufio"
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/server/db"
	"github.com/alcamerone/pocket2s/server/messaging"
	"github.com/alcamerone/pocket2s/server/room"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var testOpts = room.RoomOpts{BuyIn: 100, BigBlind: 10, SmallBlind: 5}

const origin = "http://test.local"

// ---- helpers ----

// stubStore is a db.RoomStore whose behaviour is controlled per test.
type stubStore struct {
	getRoom    func(context.Context, string) (*room.Room, error)
	newRoom    func(context.Context, *room.Room) error
	updateRoom func(context.Context, *room.Room) error
}

func (s *stubStore) GetRoom(ctx context.Context, id string) (*room.Room, error) {
	if s.getRoom != nil {
		return s.getRoom(ctx, id)
	}
	return nil, db.ErrRoomNotFound
}

func (s *stubStore) GetAllRooms(context.Context) ([]*room.Room, error) { return nil, nil }

func (s *stubStore) NewRoom(ctx context.Context, r *room.Room) error {
	if s.newRoom != nil {
		return s.newRoom(ctx, r)
	}
	return nil
}

func (s *stubStore) UpdateRoom(ctx context.Context, r *room.Room) error {
	if s.updateRoom != nil {
		return s.updateRoom(ctx, r)
	}
	return nil
}

func (s *stubStore) DeleteRoom(context.Context, string) error { return nil }

var _ db.RoomStore = (*stubStore)(nil)

// serve runs one request through the routes without websockets.
func serve(c *Context, method, path string, body io.Reader, withOrigin bool) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	AttachRoomRoutes(router, c)
	req := httptest.NewRequest(method, path, body)
	if withOrigin {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// serveWS runs one request through the routes that include /connect.
func serveWS(c *Context, path string) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	AttachRoomRoutesWithConnect(router, c)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// wsPair returns the server-side and client-side ends of a real WebSocket connection.
func wsPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	up := websocket.Upgrader{}
	ch := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		ch <- c
	}))
	t.Cleanup(srv.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-ch
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return server, client
}

func readMsg(t *testing.T, c *websocket.Conn) messaging.ToPlayerMessage {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m messaging.ToPlayerMessage
	if err := c.ReadJSON(&m); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return m
}

func send(t *testing.T, c *websocket.Conn, msg messaging.FromPlayerMessage) {
	t.Helper()
	if err := c.WriteJSON(msg); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

// seatRoom returns a started table whose seated players are given by players
// (player ID -> connection ID), seated in sorted ID order. If want is non-empty,
// that player is seated at position 1, which is where the first hand's action
// always begins (the button starts at seat 0, so seat 1 is first to act).
func seatRoom(t *testing.T, roomID string, players map[string]string, want string) *room.Room {
	t.Helper()
	order := slices.Sorted(maps.Keys(players))
	if want != "" {
		order = slices.DeleteFunc(order, func(id string) bool { return id == want })
		order = slices.Insert(order, 1, want)
	}
	r := room.NewRoom(roomID, testOpts)
	for i, pid := range order {
		r.PlayerMap[pid] = &room.Player{Id: pid, TablePos: i, ConnectionId: players[pid]}
	}
	for _, pid := range order {
		if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
			PlayerId: pid, Type: messaging.MessageTypeReady,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if want != "" && r.GameTable.Active().ID != want {
		t.Fatalf("%s should be first to act, but %s is", want, r.GameTable.Active().ID)
	}
	return r
}

// legalAction returns check or call, so a hand can progress without choices.
func legalAction(tb *table.Table) table.Action {
	for _, a := range tb.LegalActions() {
		if a == table.Check {
			return table.Action{Type: table.Check}
		}
		if a == table.Call {
			return table.Action{Type: table.Call}
		}
	}
	return table.Action{Type: table.Fold}
}

// errReader fails every read, to exercise request body read errors.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// connExistsMessenger is a Messenger whose ConnectionExists returns a fixed result.
// The embedded interface is nil, so any other method call panics, which is
// intended: these tests should only exercise ConnectionExists.
type connExistsMessenger struct {
	messaging.Messenger
	exists bool
	err    error
}

func (m connExistsMessenger) ConnectionExists(context.Context, string) (bool, error) {
	return m.exists, m.err
}

// ---- routing and headers ----

func TestSetHeaders(t *testing.T) {
	c := &Context{Rooms: &stubStore{}}
	if rec := serve(c, http.MethodGet, "/room/check/x", nil, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing origin: status %d want 400", rec.Code)
	}
	rec := serve(c, http.MethodGet, "/room/check/x", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("with origin: status %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("allow-origin = %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
		t.Fatal("allow-headers not set")
	}
}

// ---- /check ----

func TestHandleRoomCheck(t *testing.T) {
	tests := []struct {
		name string
		get  func(context.Context, string) (*room.Room, error)
		want int
	}{
		{"room exists", func(context.Context, string) (*room.Room, error) {
			return room.NewRoom("x", testOpts), nil
		}, http.StatusConflict},
		{"room missing", nil, http.StatusOK},
		{"store error", func(context.Context, string) (*room.Room, error) {
			return nil, errors.New("boom")
		}, http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Context{Rooms: &stubStore{getRoom: tt.get}}
			if rec := serve(c, http.MethodGet, "/room/check/x", nil, true); rec.Code != tt.want {
				t.Fatalf("status %d want %d", rec.Code, tt.want)
			}
		})
	}
}

// ---- /create ----

func TestHandleCreateRoom(t *testing.T) {
	body := `{"BuyIn":100,"BigBlind":10,"SmallBlind":5,"Ante":0}`
	existing := func(context.Context, string) (*room.Room, error) { return room.NewRoom("x", testOpts), nil }
	tests := []struct {
		name string
		stub *stubStore
		body io.Reader
		want int
	}{
		{"created", &stubStore{}, strings.NewReader(body), http.StatusCreated},
		{"already exists", &stubStore{getRoom: existing}, strings.NewReader(body), http.StatusConflict},
		{"lookup failure", &stubStore{getRoom: func(context.Context, string) (*room.Room, error) {
			return nil, errors.New("boom")
		}}, strings.NewReader(body), http.StatusInternalServerError},
		{"body read failure", &stubStore{}, errReader{}, http.StatusInternalServerError},
		{"invalid json", &stubStore{}, strings.NewReader("{not json"), http.StatusInternalServerError},
		{"lost race to create", &stubStore{newRoom: func(context.Context, *room.Room) error {
			return db.ErrRoomExists
		}}, strings.NewReader(body), http.StatusConflict},
		{"create failure", &stubStore{newRoom: func(context.Context, *room.Room) error {
			return errors.New("boom")
		}}, strings.NewReader(body), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Context{Rooms: tt.stub}
			if rec := serve(c, http.MethodPost, "/room/create/x", tt.body, true); rec.Code != tt.want {
				t.Fatalf("status %d want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestCreateThenCheckWithRealStore(t *testing.T) {
	c := &Context{Rooms: db.NewInMemoryRoomStore()}
	body := `{"BuyIn":100,"BigBlind":10,"SmallBlind":5}`
	if rec := serve(c, http.MethodPost, "/room/create/abc", strings.NewReader(body), true); rec.Code != http.StatusCreated {
		t.Fatalf("create status %d", rec.Code)
	}
	if rec := serve(c, http.MethodGet, "/room/check/abc", nil, true); rec.Code != http.StatusConflict {
		t.Fatalf("check after create = %d", rec.Code)
	}
}

// ---- /connect failures before the websocket upgrade ----

func TestHandleConnectFailures(t *testing.T) {
	withRoom := func(r *room.Room) db.RoomStore {
		return &stubStore{getRoom: func(context.Context, string) (*room.Room, error) { return r, nil }}
	}

	t.Run("store error", func(t *testing.T) {
		c := &Context{Rooms: &stubStore{getRoom: func(context.Context, string) (*room.Room, error) {
			return nil, errors.New("boom")
		}}}
		if rec := serveWS(c, "/room/connect/x/alice"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d", rec.Code)
		}
	})

	t.Run("nil room without error", func(t *testing.T) {
		c := &Context{Rooms: &stubStore{getRoom: func(context.Context, string) (*room.Room, error) {
			return nil, nil
		}}}
		if rec := serveWS(c, "/room/connect/x/alice"); rec.Code != http.StatusNotFound {
			t.Fatalf("status %d", rec.Code)
		}
	})

	t.Run("table full", func(t *testing.T) {
		r := room.NewRoom("x", testOpts)
		for i := 0; i <= messaging.MAX_PLAYERS; i++ {
			id := string(rune('a' + i))
			r.PlayerMap[id] = &room.Player{Id: id, TablePos: i}
		}
		c := &Context{Rooms: withRoom(r)}
		if rec := serveWS(c, "/room/connect/x/newcomer"); rec.Code != http.StatusLocked {
			t.Fatalf("status %d want 423", rec.Code)
		}
	})

	t.Run("connection lookup error for existing player", func(t *testing.T) {
		r := room.NewRoom("x", testOpts)
		r.PlayerMap["alice"] = &room.Player{Id: "alice", ConnectionId: "old"}
		c := &Context{Rooms: withRoom(r), Messenger: connExistsMessenger{err: errors.New("lookup boom")}}
		if rec := serveWS(c, "/room/connect/x/alice"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status %d", rec.Code)
		}
	})

	t.Run("existing player still connected", func(t *testing.T) {
		r := room.NewRoom("x", testOpts)
		r.PlayerMap["alice"] = &room.Player{Id: "alice", ConnectionId: "old"}
		c := &Context{Rooms: withRoom(r), Messenger: connExistsMessenger{exists: true}}
		if rec := serveWS(c, "/room/connect/x/alice"); rec.Code != http.StatusConflict {
			t.Fatalf("status %d want 409", rec.Code)
		}
	})

	t.Run("plain request fails websocket upgrade", func(t *testing.T) {
		c := &Context{Rooms: withRoom(room.NewRoom("x", testOpts)), Messenger: messaging.NewInMemoryMessenger()}
		if rec := serveWS(c, "/room/connect/x/alice"); rec.Code == http.StatusOK {
			t.Fatalf("status %d; expected the upgrade to fail", rec.Code)
		}
	})
}

// ---- /connect over a real server: full websocket flows ----

type wsHarness struct {
	srv   *httptest.Server
	store *db.InMemoryRoomStore
	msgr  *messaging.InMemoryMessenger
}

func newWSHarness(t *testing.T) *wsHarness {
	t.Helper()
	h := &wsHarness{store: db.NewInMemoryRoomStore(), msgr: messaging.NewInMemoryMessenger()}
	router := chi.NewRouter()
	AttachRoomRoutesWithConnect(router, &Context{Rooms: h.store, Messenger: h.msgr})
	h.srv = httptest.NewServer(router)
	t.Cleanup(h.srv.Close)
	if err := h.store.NewRoom(context.Background(), room.NewRoom("lobby", testOpts)); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *wsHarness) connect(roomID, playerID string) (*websocket.Conn, *http.Response, error) {
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/room/connect/" + roomID + "/" + playerID
	return websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{origin}})
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWebsocketJoinAndTableStart(t *testing.T) {
	h := newWSHarness(t)

	alice, _, err := h.connect("lobby", "alice")
	if err != nil {
		t.Fatalf("alice connect: %v", err)
	}
	defer alice.Close()
	if m := readMsg(t, alice); m.Type != messaging.MessageTypeHello {
		t.Fatalf("alice first message %v, want hello", m.Type)
	}
	if m := readMsg(t, alice); m.Type != messaging.MessageTypePlayerConnected || m.PlayerId != "alice" {
		t.Fatalf("alice self-connected broadcast wrong: %+v", m)
	}

	bob, _, err := h.connect("lobby", "bob")
	if err != nil {
		t.Fatalf("bob connect: %v", err)
	}
	defer bob.Close()
	if m := readMsg(t, bob); m.Type != messaging.MessageTypeHello {
		t.Fatalf("bob first message %v", m.Type)
	}
	readMsg(t, bob) // bob's own PlayerConnected
	if m := readMsg(t, alice); m.Type != messaging.MessageTypePlayerConnected || m.PlayerId != "bob" {
		t.Fatalf("alice should see bob connect, got %+v", m)
	}

	// Connecting again while still connected is rejected
	if _, resp, err := h.connect("lobby", "alice"); err == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate alice connect: err=%v resp=%v", err, resp)
	}

	// Ready from alice alone does not start the table, so wait for it to land
	// before bob readies, to avoid overlapping writes to the same room.
	send(t, alice, messaging.FromPlayerMessage{RoomId: "lobby", PlayerId: "alice", Type: messaging.MessageTypeReady})
	waitFor(t, "alice ready", func() bool {
		r, _ := h.store.GetRoom(context.Background(), "lobby")
		return r.PlayerMap["alice"].Ready
	})
	send(t, bob, messaging.FromPlayerMessage{RoomId: "lobby", PlayerId: "bob", Type: messaging.MessageTypeReady})

	// Both ready: table state is broadcast to both
	for _, c := range []*websocket.Conn{alice, bob} {
		if m := readMsg(t, c); m.Type != messaging.MessageTypeTableState {
			t.Fatalf("expected table state, got %+v", m)
		}
	}
}

func TestWebsocketRejoinAfterDrop(t *testing.T) {
	h := newWSHarness(t)
	alice, _, err := h.connect("lobby", "alice")
	if err != nil {
		t.Fatal(err)
	}
	readMsg(t, alice)
	readMsg(t, alice)
	alice.Close()
	waitFor(t, "server to notice the drop", func() bool {
		return !h.msgr.ConnectionExistsForRoom("lobby")
	})

	again, _, err := h.connect("lobby", "alice")
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	defer again.Close()
	if m := readMsg(t, again); m.Type != messaging.MessageTypeHello {
		t.Fatalf("rejoin should get hello, got %+v", m)
	}
	r, _ := h.store.GetRoom(context.Background(), "lobby")
	if !r.PlayerMap["alice"].SittingOut {
		t.Fatal("rejoining player should be sitting out")
	}
}

func TestWebsocketTableFull(t *testing.T) {
	h := newWSHarness(t)
	r, _ := h.store.GetRoom(context.Background(), "lobby")
	for i := 0; i <= messaging.MAX_PLAYERS; i++ {
		id := string(rune('a' + i))
		r.PlayerMap[id] = &room.Player{Id: id, TablePos: i}
	}
	if err := h.store.UpdateRoom(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, resp, err := h.connect("lobby", "zed"); err == nil || resp.StatusCode != http.StatusLocked {
		t.Fatalf("expected 423, err=%v resp=%v", err, resp)
	}
}

// ---- listenForPlayerMessages, driven over real sockets ----

// listener runs listenForPlayerMessages for one connection.
type listener struct {
	server *websocket.Conn
	client *websocket.Conn
	done   chan struct{}
}

// startListener runs the message loop for connID. If register is true the
// connection is also recorded in the messenger, as handleConnect would do.
func startListener(t *testing.T, store db.RoomStore, msgr messaging.Messenger, roomID, playerID, connID string, register bool) *listener {
	t.Helper()
	server, client := wsPair(t)
	if register {
		if err := msgr.NewConnection(context.Background(), connID, roomID, playerID, server); err != nil {
			t.Fatal(err)
		}
	}
	l := &listener{server: server, client: client, done: make(chan struct{})}
	go func() {
		listenForPlayerMessages(server, connID, store, msgr)
		close(l.done)
	}()
	return l
}

// stop closes the server side so the loop exits, and waits for it.
func (l *listener) stop(t *testing.T) {
	t.Helper()
	l.server.Close()
	select {
	case <-l.done:
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not exit")
	}
}

func TestListenBadJSONIsSkipped(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	_ = store.UpdateRoom(context.Background(), seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, ""))
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)

	if err := l.client.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if m := readMsg(t, l.client); m.Type != messaging.MessageTypeTableState {
		t.Fatalf("expected table state after hello, got %+v", m)
	}
	l.stop(t)
}

func TestListenExitsWhenRoomMissing(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "nowhere", PlayerId: "alice", Type: messaging.MessageTypeHello})
	select {
	case <-l.done:
	case <-time.After(3 * time.Second):
		t.Fatal("listener should exit when the room is missing")
	}
}

func TestListenExitsWhenConnectionClosed(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)
	l.client.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(time.Second))
	select {
	case <-l.done:
	case <-time.After(3 * time.Second):
		t.Fatal("listener should exit on a closed connection")
	}
}

func TestListenExitsOnNormalClose(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)
	// A browser closing its tab sends a normal close frame. The loop must stop
	// rather than read again, which would panic.
	_ = l.client.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	select {
	case <-l.done:
	case <-time.After(3 * time.Second):
		t.Fatal("listener should exit after a normal close")
	}
}

func TestListenIllegalActionIsReported(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	_ = store.UpdateRoom(context.Background(), r)
	l := startListener(t, store, msgr, "r1", "bob", "c-bob", true)

	// Bob is not the active player, so his check is illegal
	send(t, l.client, messaging.FromPlayerMessage{
		RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Check},
	})
	if m := readMsg(t, l.client); m.Type != messaging.MessageTypeIllegalAction {
		t.Fatalf("expected illegal action, got %+v", m)
	}
	l.stop(t)
}

func TestListenIllegalActionWithUndeliverableReply(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	_ = store.UpdateRoom(context.Background(), r)
	// The listener's connection is not registered, so the reply cannot be sent
	l := startListener(t, store, msgr, "r1", "bob", "c-bob", false)

	send(t, l.client, messaging.FromPlayerMessage{
		RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Check},
	})
	// The failed reply triggers cleanup of bob's connection; wait for that to be persisted
	waitFor(t, "bob to be marked as sitting out", func() bool {
		return mustRoom(t, store, "r1").PlayerMap["bob"].SittingOut
	})
	l.stop(t)
}

func TestListenBuyInAndReadyBeforeTableStartProduceNoBroadcast(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := room.NewRoom("r1", testOpts)
	r.PlayerMap["alice"] = &room.Player{Id: "alice", TablePos: 0, ConnectionId: "c-alice"}
	r.PlayerMap["bob"] = &room.Player{Id: "bob", TablePos: 1, ConnectionId: "c-bob", Broke: true}
	_ = store.UpdateRoom(context.Background(), r)
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)

	// Only alice is ready, so nothing is broadcast. Buy-in without a table is a no-op.
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeReady})
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypeBuyIn})
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeSitOut})

	// A hello does broadcast, so the first message received must be its response
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if m := readMsg(t, l.client); m.Type != messaging.MessageTypeTableState {
		t.Fatalf("first broadcast should be the hello response, got %+v", m)
	}
	l.stop(t)
}

func TestListenBuyInWithTableProducesNoBroadcast(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "")
	r.PlayerMap["bob"].Broke = true
	_ = store.UpdateRoom(context.Background(), r)
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)

	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypeBuyIn})
	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if m := readMsg(t, l.client); m.Type != messaging.MessageTypeTableState {
		t.Fatalf("buy-in should not broadcast; first message %+v", m)
	}
	l.stop(t)
}

func TestListenReadyStartsHandAndBroadcasts(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	ids := map[string]string{"alice": "c-alice", "bob": "c-bob"}
	// Seat the room in the lobby state (no table) with both players, not ready
	r := room.NewRoom("r1", testOpts)
	r.PlayerMap["alice"] = &room.Player{Id: "alice", TablePos: 0, ConnectionId: ids["alice"]}
	r.PlayerMap["bob"] = &room.Player{Id: "bob", TablePos: 1, ConnectionId: ids["bob"]}
	_ = store.UpdateRoom(context.Background(), r)

	aliceSrv, aliceCl := wsPair(t)
	bobSrv, bobCl := wsPair(t)
	_ = msgr.NewConnection(context.Background(), "c-alice", "r1", "alice", aliceSrv)
	_ = msgr.NewConnection(context.Background(), "c-bob", "r1", "bob", bobSrv)
	l := &listener{server: aliceSrv, client: aliceCl, done: make(chan struct{})}
	go func() {
		listenForPlayerMessages(aliceSrv, "c-alice", store, msgr)
		close(l.done)
	}()

	// Alice readies (no broadcast), then bob readies, which starts the table
	send(t, aliceCl, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeReady})
	waitFor(t, "alice ready", func() bool { return mustRoom(t, store, "r1").PlayerMap["alice"].Ready })
	send(t, aliceCl, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypeReady})
	for _, c := range []*websocket.Conn{aliceCl, bobCl} {
		if m := readMsg(t, c); m.Type != messaging.MessageTypeTableState {
			t.Fatalf("expected table state after both ready, got %+v", m)
		}
	}
	l.stop(t)
}

func TestListenPlayerActionBroadcastsToEveryone(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	_ = store.UpdateRoom(context.Background(), r)

	aliceSrv, aliceCl := wsPair(t)
	bobSrv, bobCl := wsPair(t)
	_ = msgr.NewConnection(context.Background(), "c-alice", "r1", "alice", aliceSrv)
	_ = msgr.NewConnection(context.Background(), "c-bob", "r1", "bob", bobSrv)
	l := &listener{server: aliceSrv, client: aliceCl, done: make(chan struct{})}
	go func() {
		listenForPlayerMessages(aliceSrv, "c-alice", store, msgr)
		close(l.done)
	}()

	send(t, aliceCl, messaging.FromPlayerMessage{
		RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Fold},
	})
	for _, c := range []*websocket.Conn{aliceCl, bobCl} {
		if m := readMsg(t, c); m.Type != messaging.MessageTypePlayerAction || m.PlayerAction.PlayerId != "alice" {
			t.Fatalf("expected alice's action, got %+v", m)
		}
		if m := readMsg(t, c); m.Type != messaging.MessageTypeTableState {
			t.Fatalf("expected table state, got %+v", m)
		}
	}
	l.stop(t)
}

func TestListenReportsResultOnFinishedHand(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	r := room.NewRoom("r1", testOpts)
	r.PlayerMap["alice"] = &room.Player{Id: "alice", TablePos: 0, ConnectionId: "c-alice"}
	r.GameTable = table.NewFromState(nil, table.State{
		Status: table.Done,
		Seats:  []table.Player{{ID: "alice", Chips: 100}, {ID: "bob", Chips: 100}},
		Result: table.Result{
			Winners:     []table.Player{{ID: "alice", Chips: 100}},
			Contestants: []table.Player{{ID: "alice", Chips: 100}},
			TableCards:  []hand.Card{},
		},
	})
	_ = store.UpdateRoom(context.Background(), r)
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)

	send(t, l.client, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if m := readMsg(t, l.client); m.Result == "" {
		t.Fatalf("expected a result string, got %+v", m)
	}
	l.stop(t)
}

func TestListenActionWithDisconnectedPeerCleansUp(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	// Heads-up, with bob seated on a connection the messenger does not track, so
	// broadcasts to him fail and the room must clean up after him. Alice acts first
	// (small blind) and calls; bob, as the big blind, is then to act, so the room
	// folds on his behalf, which ends the hand in alice's favour.
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	_ = store.UpdateRoom(context.Background(), r)
	l := startListener(t, store, msgr, "r1", "alice", "c-alice", true)

	send(t, l.client, messaging.FromPlayerMessage{
		RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Call},
	})
	// Alice receives her action, possibly a disconnect notice about bob, then the table state
	sawTableState := false
	for i := 0; i < 5 && !sawTableState; i++ {
		if readMsg(t, l.client).Type == messaging.MessageTypeTableState {
			sawTableState = true
		}
	}
	if !sawTableState {
		t.Fatal("expected a table state broadcast")
	}
	waitFor(t, "bob to be marked as sitting out", func() bool {
		return mustRoom(t, store, "r1").PlayerMap["bob"].SittingOut
	})
	got := mustRoom(t, store, "r1")
	if got.PlayerMap["bob"].ConnectionId != "" {
		t.Fatalf("bob's dead connection should be cleared, got %q", got.PlayerMap["bob"].ConnectionId)
	}
	var bob table.Player
	for _, seat := range got.GameTable.Seats() {
		if seat.ID == "bob" {
			bob = seat
		}
	}
	if !bob.Folded {
		t.Fatalf("bob should have been folded on his behalf: %+v", bob)
	}
	l.stop(t)
}

func mustRoom(t *testing.T, store db.RoomStore, id string) *room.Room {
	t.Helper()
	r, err := store.GetRoom(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ---- handleConnectionErrors ----

func TestHandleConnectionErrorsStaleIDsAreIgnored(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "")
	before := r.State()
	handleConnectionErrors(context.Background(), msgr, r, []string{"c-nobody"})
	if r.PlayerMap["alice"].SittingOut || r.PlayerMap["bob"].SittingOut {
		t.Fatal("stale connection IDs must not change any player")
	}
	if len(before.PlayerMap) != len(r.State().PlayerMap) {
		t.Fatal("room changed unexpectedly")
	}
}

func TestHandleConnectionErrorsActivePlayerFoldsAndOthersHearAboutIt(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	bobSrv, bobCl := wsPair(t)
	_ = msgr.NewConnection(context.Background(), "c-bob", "r1", "bob", bobSrv)

	// Alice is the active player and drops, so she is folded on her behalf
	handleConnectionErrors(context.Background(), msgr, r, []string{"c-alice"})

	if !r.PlayerMap["alice"].SittingOut || r.PlayerMap["alice"].ConnectionId != "" {
		t.Fatalf("alice not marked disconnected: %+v", r.PlayerMap["alice"])
	}
	if !r.GetPlayerState("alice").Folded {
		t.Fatal("alice should have been folded")
	}
	if m := readMsg(t, bobCl); m.Type != messaging.MessageTypePlayerDisconnected || m.PlayerId != "alice" {
		t.Fatalf("bob should hear about the disconnect, got %+v", m)
	}
}

func TestHandleConnectionErrorsInactivePlayerIsNotFolded(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	aliceSrv, aliceCl := wsPair(t)
	_ = msgr.NewConnection(context.Background(), "c-alice", "r1", "alice", aliceSrv)

	// Bob is not to act, so dropping him only announces the disconnect
	handleConnectionErrors(context.Background(), msgr, r, []string{"c-bob"})

	if m := readMsg(t, aliceCl); m.Type != messaging.MessageTypePlayerDisconnected || m.PlayerId != "bob" {
		t.Fatalf("alice should hear that bob dropped, got %+v", m)
	}
	// No fold and no table state follow
	_ = aliceCl.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var extra messaging.ToPlayerMessage
	if err := aliceCl.ReadJSON(&extra); err == nil {
		t.Fatalf("unexpected extra message %+v", extra)
	}
	if r.GameTable.Active().ID != "alice" {
		t.Fatal("the active player must not change when an inactive player drops")
	}
}

func TestHandleConnectionErrorsRecursesOnFailedBroadcast(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	// Alice is active and drops; bob's connection is not tracked, so the
	// disconnect broadcast fails for him and the handler must recurse.
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, "alice")
	handleConnectionErrors(context.Background(), msgr, r, []string{"c-alice"})
	if !r.PlayerMap["bob"].SittingOut {
		t.Fatalf("bob should be marked sitting out after the failed broadcast")
	}
}

// ---- broadcast ----

func TestBroadcastSkipsDisconnectedAndCollectsFailures(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "", "bob": "c-bob", "carol": "c-car"}, "")
	errs := broadcast(context.Background(), msgr, r, messaging.ToPlayerMessage{Type: messaging.MessageTypePlayerConnected})
	if len(errs) != 2 {
		t.Fatalf("expected two failed connections, got %v", errs)
	}
}

func TestBroadcastTableStatePerPlayer(t *testing.T) {
	msgr := messaging.NewInMemoryMessenger()
	r := seatRoom(t, "r1", map[string]string{"alice": "c-alice", "bob": ""}, "")
	aliceSrv, aliceCl := wsPair(t)
	_ = msgr.NewConnection(context.Background(), "c-alice", "r1", "alice", aliceSrv)

	errs := broadcast(context.Background(), msgr, r, messaging.ToPlayerMessage{
		Type:       messaging.MessageTypeTableState,
		TableState: r.GetObfuscatedTableState(),
	})
	if errs != nil {
		t.Fatalf("unexpected failures: %v", errs)
	}
	m := readMsg(t, aliceCl)
	if m.Type != messaging.MessageTypeTableState || m.PlayerState.ID != "alice" {
		t.Fatalf("alice should receive her own state, got %+v", m)
	}
}

// failAfterWrites wraps a connection so that writes fail once `allowed` writes
// have succeeded, simulating a client that drops right after the handshake.
type failAfterWrites struct {
	net.Conn
	mu      sync.Mutex
	allowed int
}

func (c *failAfterWrites) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.allowed <= 0 {
		return 0, errors.New("use of closed network connection")
	}
	c.allowed--
	return c.Conn.Write(p)
}

// hijackWriter hands out failAfterWrites connections when the handler hijacks.
type hijackWriter struct {
	http.ResponseWriter
	allowed int
}

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &failAfterWrites{Conn: conn, allowed: w.allowed}, rw, nil
}

func TestWebsocketHelloSendFailureCleansUpPlayer(t *testing.T) {
	ctx := context.Background()
	store := db.NewInMemoryRoomStore()
	msgr := messaging.NewInMemoryMessenger()
	if err := store.NewRoom(ctx, room.NewRoom("lobby", testOpts)); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	AttachRoomRoutesWithConnect(router, &Context{Rooms: store, Messenger: msgr})
	// Only the handshake write succeeds; the hello and everything after it fail
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(&hijackWriter{ResponseWriter: w, allowed: 1}, r)
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/room/connect/lobby/alice"
	c, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": []string{origin}})
	if err != nil {
		t.Fatalf("handshake should succeed: %v", err)
	}
	defer c.Close()

	// The failed sends mark alice as disconnected in the room
	waitFor(t, "alice to be marked disconnected", func() bool {
		r, _ := store.GetRoom(ctx, "lobby")
		p := r.PlayerMap["alice"]
		return p != nil && p.SittingOut && p.ConnectionId == ""
	})
}

func TestBroadcastMarksBrokePlayersAtShowdown(t *testing.T) {
	ctx := context.Background()
	msgr := messaging.NewInMemoryMessenger()
	aliceSrv, aliceCl := wsPair(t)
	_ = msgr.NewConnection(ctx, "c-alice", "r1", "alice", aliceSrv)

	r := room.NewRoom("r1", testOpts)
	r.PlayerMap["alice"] = &room.Player{Id: "alice", ConnectionId: "c-alice"}
	r.GameTable = table.NewFromState(nil, table.State{
		Status: table.Done,
		Seats:  []table.Player{{ID: "alice", Chips: 0}, {ID: "bob", Chips: 100}},
	})

	if errs := broadcast(ctx, msgr, r, messaging.ToPlayerMessage{
		Type:       messaging.MessageTypeTableState,
		TableState: r.GetObfuscatedTableState(),
	}); errs != nil {
		t.Fatalf("unexpected failures: %v", errs)
	}
	if m := readMsg(t, aliceCl); m.PlayerState.Chips != 0 {
		t.Fatalf("alice's state should show no chips, got %+v", m.PlayerState)
	}
	if !r.PlayerMap["alice"].Broke {
		t.Fatal("a player with no chips at a finished table should be marked broke")
	}
}
