package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/server/db"
	"github.com/alcamerone/pocket2s/server/messaging"
	"github.com/alcamerone/pocket2s/server/room"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	apigwTypes "github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ---- fake AWS clients: DynamoDB connection mappings and the management API ----

var errBackend = errors.New("backend failure")

// fakeDDB is an in-memory stand-in for the DynamoDB client.
type fakeDDB struct {
	mu    sync.Mutex
	items map[string]map[string]ddbTypes.AttributeValue // connection ID -> stored item
	err   bool                                          // every call fails
}

func connKey(av ddbTypes.AttributeValue) string {
	return av.(*ddbTypes.AttributeValueMemberS).Value
}

func (d *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err {
		return nil, errBackend
	}
	d.items[connKey(in.Item["ConnectionId"])] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (d *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err {
		return nil, errBackend
	}
	return &dynamodb.GetItemOutput{Item: d.items[connKey(in.Key["ConnectionId"])]}, nil
}

func (d *fakeDDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err {
		return nil, errBackend
	}
	// Only the ConnectionId key is recognised, as in the table schema
	if key, ok := in.Key["ConnectionId"]; ok {
		delete(d.items, connKey(key))
	}
	return &dynamodb.DeleteItemOutput{}, nil
}

// fakeAPIGW is an in-memory stand-in for the API Gateway management client.
type fakeAPIGW struct {
	mu     sync.Mutex
	live   map[string]bool // connections that exist
	gone   map[string]bool // connections that reject posts as gone
	sent   map[string][]messaging.ToPlayerMessage
	getErr bool // existence checks fail
}

func (g *fakeAPIGW) GetConnection(_ context.Context, in *apigatewaymanagementapi.GetConnectionInput, _ ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.GetConnectionOutput, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.getErr {
		return nil, errBackend
	}
	if !g.live[*in.ConnectionId] {
		return nil, &apigwTypes.GoneException{}
	}
	return &apigatewaymanagementapi.GetConnectionOutput{}, nil
}

func (g *fakeAPIGW) PostToConnection(_ context.Context, in *apigatewaymanagementapi.PostToConnectionInput, _ ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.PostToConnectionOutput, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := *in.ConnectionId
	if g.gone[id] {
		return nil, &apigwTypes.GoneException{}
	}
	var msg messaging.ToPlayerMessage
	_ = json.Unmarshal(in.Data, &msg)
	g.sent[id] = append(g.sent[id], msg)
	return &apigatewaymanagementapi.PostToConnectionOutput{}, nil
}

// sentTo returns the messages posted to a connection so far.
func (g *fakeAPIGW) sentTo(id string) []messaging.ToPlayerMessage {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]messaging.ToPlayerMessage(nil), g.sent[id]...)
}

// fakeBackend is the pair of fake clients behind the messenger under test.
type fakeBackend struct {
	ddb *fakeDDB
	gw  *fakeAPIGW
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	b := &fakeBackend{
		ddb: &fakeDDB{items: map[string]map[string]ddbTypes.AttributeValue{}},
		gw: &fakeAPIGW{
			live: map[string]bool{},
			gone: map[string]bool{},
			sent: map[string][]messaging.ToPlayerMessage{},
		},
	}
	// Route the package globals through this backend for the duration of the test
	origStore, origMessenger := roomStore, messenger
	t.Cleanup(func() { roomStore, messenger = origStore, origMessenger })
	messenger = messaging.NewLambdaMessenger(b.gw, b.ddb, "conns")
	return b
}

// ---- room store stub ----

type stubStore struct {
	getRoom    func(context.Context, string) (*room.Room, error)
	updateRoom func(context.Context, *room.Room) error
}

func (s *stubStore) GetRoom(ctx context.Context, id string) (*room.Room, error) {
	if s.getRoom != nil {
		return s.getRoom(ctx, id)
	}
	return nil, db.ErrRoomNotFound
}
func (s *stubStore) GetAllRooms(context.Context) ([]*room.Room, error) { return nil, nil }
func (s *stubStore) NewRoom(context.Context, *room.Room) error         { return nil }
func (s *stubStore) UpdateRoom(ctx context.Context, r *room.Room) error {
	if s.updateRoom != nil {
		return s.updateRoom(ctx, r)
	}
	return nil
}
func (s *stubStore) DeleteRoom(context.Context, string) error { return nil }

var _ db.RoomStore = (*stubStore)(nil)

// useStore installs a room store for the test and restores the previous one on cleanup.
func useStore(t *testing.T, s db.RoomStore) {
	t.Helper()
	orig := roomStore
	t.Cleanup(func() { roomStore = orig })
	roomStore = s
}

// ---- helpers ----

// seedRoom stores a room with the given players (ID -> connection ID). The table
// is started once every player is ready, when startTable is true.
func seedRoom(t *testing.T, store db.RoomStore, roomID string, players map[string]string, startTable bool) *room.Room {
	t.Helper()
	r := room.NewRoom(roomID, room.RoomOpts{BuyIn: 100, BigBlind: 10, SmallBlind: 5})
	order := []string{}
	for pid := range players {
		order = append(order, pid)
	}
	for i, pid := range order {
		r.PlayerMap[pid] = &room.Player{Id: pid, TablePos: i, ConnectionId: players[pid]}
	}
	if startTable {
		for _, pid := range order {
			if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: pid, Type: messaging.MessageTypeReady}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.NewRoom(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

// registerConn records a connection in the messenger's mapping table and marks it live.
func registerConn(t *testing.T, b *fakeBackend, connID, roomID, playerID string) {
	t.Helper()
	if err := messenger.NewConnection(context.Background(), connID, roomID, playerID, nil); err != nil {
		t.Fatal(err)
	}
	b.gw.mu.Lock()
	b.gw.live[connID] = true
	b.gw.mu.Unlock()
}

func setGone(b *fakeBackend, connIDs ...string) {
	b.gw.mu.Lock()
	defer b.gw.mu.Unlock()
	for _, id := range connIDs {
		b.gw.gone[id] = true
	}
}

func getRoom(t *testing.T, store db.RoomStore, id string) *room.Room {
	t.Helper()
	r, err := store.GetRoom(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func connectEvent(roomID, playerID, connID string) events.APIGatewayWebsocketProxyRequest {
	return events.APIGatewayWebsocketProxyRequest{
		QueryStringParameters: map[string]string{"roomId": roomID, "playerId": playerID},
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{
			RouteKey: "$connect", ConnectionID: connID,
		},
	}
}

func disconnectEvent(connID string) events.APIGatewayWebsocketProxyRequest {
	return events.APIGatewayWebsocketProxyRequest{
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{
			RouteKey: "$disconnect", ConnectionID: connID,
		},
	}
}

func messageEvent(connID string, msg messaging.FromPlayerMessage) events.APIGatewayWebsocketProxyRequest {
	body, _ := json.Marshal(msg)
	return events.APIGatewayWebsocketProxyRequest{
		Body: string(body),
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{
			RouteKey: "$default", ConnectionID: connID,
		},
	}
}

// statusOfT returns a checker that fails the test on error and yields the status code.
func statusOfT(t *testing.T) func(events.APIGatewayProxyResponse, error) int {
	return func(resp events.APIGatewayProxyResponse, err error) int {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return resp.StatusCode
	}
}

// legalAction returns check or call when possible, so a hand can progress without folds.
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

// ---- routing and retries ----

func TestHandlerRoutesByRouteKey(t *testing.T) {
	newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{}, false)

	// $connect creates the player record
	if got := statusOfT(t)(handler(context.Background(), connectEvent("r1", "alice", "c-alice"))); got != http.StatusOK {
		t.Fatalf("connect status %d", got)
	}
	if _, _, err := messenger.GetRoomAndPlayerByConnectionId(context.Background(), "c-alice"); err != nil {
		t.Fatalf("connection mapping missing: %v", err)
	}
	// $disconnect for a connection that is already gone succeeds
	if got := statusOfT(t)(handler(context.Background(), disconnectEvent("c-nobody"))); got != http.StatusOK {
		t.Fatalf("disconnect status %d", got)
	}
	// Any other route is a player message, here a ping
	ping := messageEvent("c-alice", messaging.FromPlayerMessage{Type: messaging.MessageTypePing})
	if got := statusOfT(t)(handler(context.Background(), ping)); got != http.StatusOK {
		t.Fatalf("default route status %d", got)
	}
}

func TestWithVersioningRetriesGivesUpAfterConflicts(t *testing.T) {
	calls := 0
	h := func(context.Context, events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		calls++
		return events.APIGatewayProxyResponse{}, db.ErrVersionConflict
	}
	resp, err := withVersioningRetries(context.Background(), events.APIGatewayWebsocketProxyRequest{}, h)
	if err != nil || resp.StatusCode != errResponse.StatusCode {
		t.Fatalf("got %+v, %v", resp, err)
	}
	if calls != retryCount {
		t.Fatalf("attempts = %d want %d", calls, retryCount)
	}
}

func TestWithVersioningRetriesSucceedsAfterConflict(t *testing.T) {
	calls := 0
	h := func(context.Context, events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		calls++
		if calls == 1 {
			return events.APIGatewayProxyResponse{}, db.ErrVersionConflict
		}
		return successResponse, nil
	}
	resp, err := withVersioningRetries(context.Background(), events.APIGatewayWebsocketProxyRequest{}, h)
	if err != nil || resp.StatusCode != http.StatusOK || calls != 2 {
		t.Fatalf("resp=%+v err=%v calls=%d", resp, err, calls)
	}
}

func TestWithVersioningRetriesPassesOtherErrorsThrough(t *testing.T) {
	boom := errors.New("boom")
	h := func(context.Context, events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		return errResponse, boom
	}
	if _, err := withVersioningRetries(context.Background(), events.APIGatewayWebsocketProxyRequest{}, h); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithVersioningRetriesStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := func(context.Context, events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
		return events.APIGatewayProxyResponse{}, db.ErrVersionConflict
	}
	resp, err := withVersioningRetries(ctx, events.APIGatewayWebsocketProxyRequest{}, h)
	if err != nil || resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

// ---- $connect ----

func TestConnectValidation(t *testing.T) {
	newFakeBackend(t)
	useStore(t, db.NewInMemoryRoomStore())
	if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("", "alice", "c"))); got != http.StatusBadRequest {
		t.Fatalf("missing room: %d", got)
	}
	if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("r", "", "c"))); got != http.StatusBadRequest {
		t.Fatalf("missing player: %d", got)
	}
}

func TestConnectRoomLookup(t *testing.T) {
	newFakeBackend(t)

	t.Run("room does not exist", func(t *testing.T) {
		useStore(t, db.NewInMemoryRoomStore())
		if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("nope", "alice", "c"))); got != http.StatusGone {
			t.Fatalf("status %d want 410", got)
		}
	})

	t.Run("store failure", func(t *testing.T) {
		useStore(t, &stubStore{getRoom: func(context.Context, string) (*room.Room, error) {
			return nil, errors.New("boom")
		}})
		if _, err := handleConnect(context.Background(), connectEvent("r", "alice", "c")); err == nil {
			t.Fatal("expected store error to propagate")
		}
	})
}

func TestConnectExistingPlayerChecks(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "old"}, false)

	t.Run("live connection is rejected", func(t *testing.T) {
		b.gw.mu.Lock()
		b.gw.live["old"] = true
		b.gw.mu.Unlock()
		if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("r1", "alice", "new"))); got != http.StatusConflict {
			t.Fatalf("status %d want 409", got)
		}
		b.gw.mu.Lock()
		b.gw.live["old"] = false
		b.gw.mu.Unlock()
	})

	t.Run("liveness check failure", func(t *testing.T) {
		b.gw.mu.Lock()
		b.gw.getErr = true
		b.gw.mu.Unlock()
		if _, err := handleConnect(context.Background(), connectEvent("r1", "alice", "new")); err == nil {
			t.Fatal("expected error from liveness check")
		}
		b.gw.mu.Lock()
		b.gw.getErr = false
		b.gw.mu.Unlock()
	})
}

func TestConnectTableFull(t *testing.T) {
	newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	players := map[string]string{}
	for i := 0; i < messaging.MAX_PLAYERS; i++ {
		players[string(rune('a'+i))] = ""
	}
	seedRoom(t, store, "full", players, false)
	if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("full", "newcomer", "c"))); got != http.StatusLocked {
		t.Fatalf("status %d want 423", got)
	}
}

func TestConnectNewPlayer(t *testing.T) {
	newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{}, false)

	if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("r1", "alice", "c-alice"))); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	r := getRoom(t, store, "r1")
	if r.PlayerMap["alice"] == nil || r.PlayerMap["alice"].ConnectionId != "c-alice" {
		t.Fatalf("player not recorded: %+v", r.PlayerMap)
	}
}

func TestConnectRejoinPlayer(t *testing.T) {
	newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": ""}, false) // alice has dropped

	if got := statusOfT(t)(handleConnect(context.Background(), connectEvent("r1", "alice", "c-new"))); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	p := getRoom(t, store, "r1").PlayerMap["alice"]
	if p.ConnectionId != "c-new" || !p.SittingOut || p.Ready {
		t.Fatalf("rejoin state wrong: %+v", p)
	}
}

func TestConnectStoreFailures(t *testing.T) {
	t.Run("connection record cannot be stored", func(t *testing.T) {
		b := newFakeBackend(t)
		useStore(t, db.NewInMemoryRoomStore())
		seedRoom(t, roomStore, "r1", map[string]string{}, false)
		b.ddb.mu.Lock()
		b.ddb.err = true
		b.ddb.mu.Unlock()
		if _, err := handleConnect(context.Background(), connectEvent("r1", "alice", "c")); err == nil {
			t.Fatal("expected error when the connection record fails")
		}
	})

	t.Run("room cannot be persisted", func(t *testing.T) {
		newFakeBackend(t)
		store := db.NewInMemoryRoomStore()
		seedRoom(t, store, "r1", map[string]string{}, false)
		useStore(t, &stubStore{
			getRoom: func(ctx context.Context, id string) (*room.Room, error) {
				return store.GetRoom(ctx, id)
			},
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		if _, err := handleConnect(context.Background(), connectEvent("r1", "alice", "c")); err == nil {
			t.Fatal("expected error when the room cannot be saved")
		}
	})
}

// ---- $disconnect ----

func TestDisconnectUnknownConnectionIsSuccess(t *testing.T) {
	newFakeBackend(t)
	useStore(t, db.NewInMemoryRoomStore())
	if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("ghost"))); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
}

func TestDisconnectMappingLookupFailure(t *testing.T) {
	b := newFakeBackend(t)
	useStore(t, db.NewInMemoryRoomStore())
	b.ddb.mu.Lock()
	b.ddb.err = true
	b.ddb.mu.Unlock()
	if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c"))); got != http.StatusInternalServerError {
		t.Fatalf("status %d", got)
	}
}

func TestDisconnectRoomCases(t *testing.T) {
	t.Run("room gone", func(t *testing.T) {
		b := newFakeBackend(t)
		useStore(t, db.NewInMemoryRoomStore())
		registerConn(t, b, "c", "missing", "alice")
		if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c"))); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
	})

	t.Run("room lookup failure", func(t *testing.T) {
		b := newFakeBackend(t)
		useStore(t, &stubStore{getRoom: func(context.Context, string) (*room.Room, error) {
			return nil, errors.New("boom")
		}})
		registerConn(t, b, "c", "r1", "alice")
		if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c"))); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})

	t.Run("state cannot be saved", func(t *testing.T) {
		b := newFakeBackend(t)
		store := db.NewInMemoryRoomStore()
		seedRoom(t, store, "r1", map[string]string{"alice": "c"}, false)
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, id string) (*room.Room, error) { return store.GetRoom(ctx, id) },
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		registerConn(t, b, "c", "r1", "alice")
		if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c"))); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})

	t.Run("player is marked and others told", func(t *testing.T) {
		b := newFakeBackend(t)
		store := db.NewInMemoryRoomStore()
		useStore(t, store)
		seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
		registerConn(t, b, "c-alice", "r1", "alice")
		registerConn(t, b, "c-bob", "r1", "bob")

		if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c-alice"))); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		r := getRoom(t, store, "r1")
		if !r.PlayerMap["alice"].SittingOut || r.PlayerMap["alice"].ConnectionId != "" {
			t.Fatalf("alice not marked: %+v", r.PlayerMap["alice"])
		}
		msgs := b.gw.sentTo("c-bob")
		if len(msgs) != 1 || msgs[0].Type != messaging.MessageTypePlayerDisconnected || msgs[0].PlayerId != "alice" {
			t.Fatalf("bob should be told about alice: %+v", msgs)
		}
	})
}

// ---- default route: player messages ----

func TestMessageRejectsBadBody(t *testing.T) {
	newFakeBackend(t)
	useStore(t, db.NewInMemoryRoomStore())
	ev := events.APIGatewayWebsocketProxyRequest{
		Body:           "{not json",
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{ConnectionID: "c"},
	}
	if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusBadRequest {
		t.Fatalf("status %d", got)
	}
}

func TestMessagePingIsAcknowledged(t *testing.T) {
	newFakeBackend(t)
	useStore(t, db.NewInMemoryRoomStore())
	ping := messageEvent("c", messaging.FromPlayerMessage{Type: messaging.MessageTypePing})
	if got := statusOfT(t)(handleMessage(context.Background(), ping)); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
}

func TestMessageIdentityChecks(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")

	t.Run("unknown connection", func(t *testing.T) {
		ev := messageEvent("c-ghost", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
		if _, err := handleMessage(context.Background(), ev); err == nil {
			t.Fatal("expected error for unknown connection")
		}
	})

	t.Run("message claims another player", func(t *testing.T) {
		ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypeHello})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusForbidden {
			t.Fatalf("status %d want 403", got)
		}
	})

	t.Run("room missing", func(t *testing.T) {
		b.ddb.mu.Lock()
		b.ddb.items["c-gone"] = map[string]ddbTypes.AttributeValue{
			"ConnectionId": &ddbTypes.AttributeValueMemberS{Value: "c-gone"},
			"RoomId":       &ddbTypes.AttributeValueMemberS{Value: "nope"},
			"PlayerId":     &ddbTypes.AttributeValueMemberS{Value: "alice"},
		}
		b.ddb.mu.Unlock()
		ev := messageEvent("c-gone", messaging.FromPlayerMessage{RoomId: "nope", PlayerId: "alice", Type: messaging.MessageTypeHello})
		if _, err := handleMessage(context.Background(), ev); err == nil {
			t.Fatal("expected error when the room is missing")
		}
	})
}

func TestMessageHelloSendsStateAndAnnounces(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")

	ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if msgs := b.gw.sentTo("c-alice"); len(msgs) != 2 || msgs[0].Type != messaging.MessageTypeHello {
		t.Fatalf("alice should get hello state first: %+v", msgs)
	}
	if msgs := b.gw.sentTo("c-bob"); len(msgs) != 1 || msgs[0].Type != messaging.MessageTypePlayerConnected {
		t.Fatalf("bob should be told alice is here: %+v", msgs)
	}
}

func TestMessageHelloToDeadConnectionCleansUp(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")
	setGone(b, "c-alice") // alice's own socket is dead when we reply

	ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if !getRoom(t, store, "r1").PlayerMap["alice"].SittingOut {
		t.Fatal("alice should be sitting out after her connection died")
	}
}

func TestMessageHelloBroadcastFailureCleansUpOthers(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")
	setGone(b, "c-bob")

	ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
	if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if !getRoom(t, store, "r1").PlayerMap["bob"].SittingOut {
		t.Fatal("bob should be sitting out after failing to receive the broadcast")
	}
}

func TestMessageHelloFailuresSurface(t *testing.T) {
	t.Run("cleanup after dead hello fails", func(t *testing.T) {
		b := newFakeBackend(t)
		store := db.NewInMemoryRoomStore()
		seedRoom(t, store, "r1", map[string]string{"alice": "c-alice"}, false)
		registerConn(t, b, "c-alice", "r1", "alice")
		setGone(b, "c-alice")
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, id string) (*room.Room, error) { return store.GetRoom(ctx, id) },
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})

	t.Run("cleanup after failed announcement fails", func(t *testing.T) {
		b := newFakeBackend(t)
		store := db.NewInMemoryRoomStore()
		seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
		registerConn(t, b, "c-alice", "r1", "alice")
		registerConn(t, b, "c-bob", "r1", "bob")
		setGone(b, "c-bob")
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, id string) (*room.Room, error) { return store.GetRoom(ctx, id) },
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		ev := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeHello})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})
}

// seatedGame returns a store with alice and bob seated at a started table, both connected.
func seatedGame(t *testing.T, b *fakeBackend) db.RoomStore {
	t.Helper()
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, true)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")
	return store
}

// seatedTrio seats alice, bob and carol at a started table, with want to act first.
// Three seats keep the hand alive when one player folds, so no payout is reached.
func seatedTrio(t *testing.T, b *fakeBackend, want string) db.RoomStore {
	t.Helper()
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	players := map[string]string{"alice": "c-alice", "bob": "c-bob", "carol": "c-carol"}
	for range 50 {
		seedRoom(t, store, "r1", players, true)
		if getRoom(t, store, "r1").GameTable.Active().ID == want {
			break
		}
	}
	for pid, conn := range players {
		registerConn(t, b, conn, "r1", pid)
	}
	return store
}

// actorConn returns the connection and player ID of whoever is to act.
func actorConn(t *testing.T, store db.RoomStore) (string, string) {
	t.Helper()
	id := getRoom(t, store, "r1").GameTable.Active().ID
	return "c-" + id, id
}

func TestMessageReadyAndBuyInProduceNoBroadcastUntilStart(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")

	ready := messageEvent("c-alice", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "alice", Type: messaging.MessageTypeReady})
	if got := statusOfT(t)(handleMessage(context.Background(), ready)); got != http.StatusOK {
		t.Fatalf("ready status %d", got)
	}
	if len(b.gw.sentTo("c-alice"))+len(b.gw.sentTo("c-bob")) != 0 {
		t.Fatal("nothing should be broadcast while players are still getting ready")
	}
}

func TestMessageReadyStartsTableAndBroadcasts(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")

	for _, pid := range []string{"alice", "bob"} {
		ev := messageEvent("c-"+pid, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: pid, Type: messaging.MessageTypeReady})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("ready status %d", got)
		}
	}
	if msgs := b.gw.sentTo("c-bob"); len(msgs) != 1 || msgs[0].Type != messaging.MessageTypeTableState {
		t.Fatalf("bob should receive the table start: %+v", msgs)
	}
}

func TestMessageBuyInAtTable(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedGame(t, b)
	r := getRoom(t, store, "r1")
	r.PlayerMap["bob"].Broke = true
	_ = store.UpdateRoom(context.Background(), r)

	ev := messageEvent("c-bob", messaging.FromPlayerMessage{RoomId: "r1", PlayerId: "bob", Type: messaging.MessageTypeBuyIn})
	if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
		t.Fatalf("status %d", got)
	}
	if getRoom(t, store, "r1").PlayerMap["bob"].Broke {
		t.Fatal("buy-in should clear the broke flag")
	}
}

func TestMessageActionFlows(t *testing.T) {
	t.Run("legal action is broadcast with table state", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		conn, id := actorConn(t, store)
		tb := getRoom(t, store, "r1").GameTable
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction, Action: legalAction(tb),
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		other := "c-bob"
		if id == "bob" {
			other = "c-alice"
		}
		msgs := b.gw.sentTo(other)
		if len(msgs) < 2 || msgs[0].Type != messaging.MessageTypePlayerAction || msgs[1].Type != messaging.MessageTypeTableState {
			t.Fatalf("other player should see the action then table state: %+v", msgs)
		}
	})

	t.Run("illegal action is reported to the sender", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		_, id := actorConn(t, store)
		// Act as the player who is not to move
		other, otherID := "c-bob", "bob"
		if id == "bob" {
			other, otherID = "c-alice", "alice"
		}
		ev := messageEvent(other, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: otherID, Type: messaging.MessageTypePlayerAction,
			Action: table.Action{Type: table.Check},
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		if msgs := b.gw.sentTo(other); len(msgs) != 1 || msgs[0].Type != messaging.MessageTypeIllegalAction {
			t.Fatalf("expected an illegal action reply: %+v", msgs)
		}
	})

	t.Run("illegal action to a dead connection cleans up", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		_, id := actorConn(t, store)
		other, otherID := "c-bob", "bob"
		if id == "bob" {
			other, otherID = "c-alice", "alice"
		}
		setGone(b, other)
		ev := messageEvent(other, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: otherID, Type: messaging.MessageTypePlayerAction,
			Action: table.Action{Type: table.Check},
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		if !getRoom(t, store, "r1").PlayerMap[otherID].SittingOut {
			t.Fatal("player should be sitting out after their connection died")
		}
	})

	t.Run("illegal action where cleanup cannot be saved fails", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		_, id := actorConn(t, store)
		other, otherID := "c-bob", "bob"
		if id == "bob" {
			other, otherID = "c-alice", "alice"
		}
		setGone(b, other)
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		ev := messageEvent(other, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: otherID, Type: messaging.MessageTypePlayerAction,
			Action: table.Action{Type: table.Check},
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})

	t.Run("state cannot be saved after a legal action", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		conn, id := actorConn(t, store)
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
			updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") },
		})
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction,
			Action: legalAction(getRoom(t, store, "r1").GameTable),
		})
		resp, err := handleMessage(context.Background(), ev)
		if err == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
	})

	t.Run("action announcement fails for the sender", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		conn, id := actorConn(t, store)
		// The actor's own socket is dead. Their turn has passed by the time we
		// clean up, so nobody is folded on their behalf.
		setGone(b, conn)
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction,
			Action: legalAction(getRoom(t, store, "r1").GameTable),
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("status %d", got)
		}
		if !getRoom(t, store, "r1").PlayerMap[id].SittingOut {
			t.Fatal("sender should be sitting out after their connection died")
		}
	})

	t.Run("action announcement cleanup cannot be saved", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		conn, id := actorConn(t, store)
		setGone(b, conn)
		// Save 1 is the action itself and succeeds; save 2 is the cleanup and fails
		calls := 0
		useStore(t, &stubStore{
			getRoom: func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
			updateRoom: func(ctx context.Context, rr *room.Room) error {
				calls++
				if calls == 1 {
					return store.UpdateRoom(ctx, rr)
				}
				return errors.New("boom")
			},
		})
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction,
			Action: legalAction(getRoom(t, store, "r1").GameTable),
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusInternalServerError {
			t.Fatalf("status %d", got)
		}
	})
}

// playHandToDone plays check/call actions through the handler until the hand
// reaches showdown, so the final table state is broadcast with a result.
func playHandToDone(t *testing.T, b *fakeBackend, store db.RoomStore) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if getRoom(t, store, "r1").GameTable.State().Status == table.Done {
			return
		}
		conn, id := actorConn(t, store)
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction,
			Action: legalAction(getRoom(t, store, "r1").GameTable),
		})
		if got := statusOfT(t)(handleMessage(context.Background(), ev)); got != http.StatusOK {
			t.Fatalf("action status %d", got)
		}
	}
	t.Fatal("hand did not finish")
}

func TestMessageFinishedHandReportsResult(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedGame(t, b)
	playHandToDone(t, b, store)

	// The final table state broadcast carries the outcome for both players
	msgs := b.gw.sentTo("c-bob")
	last := msgs[len(msgs)-1]
	if last.Type != messaging.MessageTypeTableState || last.Result == "" {
		t.Fatalf("expected the final table state to carry a result, got %+v", last)
	}
}

func TestMessageTableStartBroadcastCleanupFails(t *testing.T) {
	b := newFakeBackend(t)
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, false)
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")
	setGone(b, "c-bob")

	// Save 1 is alice's ready and save 2 is bob's ready. Save 3 is the cleanup
	// after bob cannot receive the table start, which is the one that fails.
	calls := 0
	useStore(t, &stubStore{
		getRoom: func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
		updateRoom: func(ctx context.Context, rr *room.Room) error {
			calls++
			if calls == 3 {
				return errors.New("boom")
			}
			return store.UpdateRoom(ctx, rr)
		},
	})
	for _, pid := range []string{"alice", "bob"} {
		ev := messageEvent("c-"+pid, messaging.FromPlayerMessage{RoomId: "r1", PlayerId: pid, Type: messaging.MessageTypeReady})
		got := statusOfT(t)(handleMessage(context.Background(), ev))
		if pid == "bob" && got != http.StatusInternalServerError {
			t.Fatalf("bob's ready should fail when cleanup cannot be saved, got %d", got)
		}
	}
}

func TestDisconnectOnRiverEndsHandWithResult(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedGame(t, b)

	// Check or call through to the river
	for i := 0; i < 50 && getRoom(t, store, "r1").GameTable.State().Round != table.River; i++ {
		conn, id := actorConn(t, store)
		ev := messageEvent(conn, messaging.FromPlayerMessage{
			RoomId: "r1", PlayerId: id, Type: messaging.MessageTypePlayerAction,
			Action: legalAction(getRoom(t, store, "r1").GameTable),
		})
		statusOfT(t)(handleMessage(context.Background(), ev))
	}
	if getRoom(t, store, "r1").GameTable.State().Round != table.River {
		t.Fatal("expected to reach the river")
	}

	// The first player checks on the river, leaving the second to act
	firstConn, firstID := actorConn(t, store)
	secondID := "bob"
	if firstID == "bob" {
		secondID = "alice"
	}
	ev := messageEvent(firstConn, messaging.FromPlayerMessage{
		RoomId: "r1", PlayerId: firstID, Type: messaging.MessageTypePlayerAction,
		Action: legalAction(getRoom(t, store, "r1").GameTable),
	})
	statusOfT(t)(handleMessage(context.Background(), ev))

	// The second player drops while to act, so they fold and the hand is paid out
	if got := statusOfT(t)(handleDisconnect(context.Background(), disconnectEvent("c-"+secondID))); got != http.StatusOK {
		t.Fatalf("disconnect status %d", got)
	}
	msgs := b.gw.sentTo("c-" + firstID)
	last := msgs[len(msgs)-1]
	if last.Type != messaging.MessageTypeTableState || last.Result == "" {
		t.Fatalf("expected the final table state to carry the result, got %+v", last)
	}
}

// ---- handleConnectionErrors ----

func TestConnectionErrorsIgnoresStaleIDs(t *testing.T) {
	store := db.NewInMemoryRoomStore()
	useStore(t, store)
	r := seedRoom(t, store, "r1", map[string]string{"alice": "c-alice", "bob": "c-bob"}, true)
	out, err := handleConnectionErrors(context.Background(), r, []string{"c-nobody"})
	if err != nil || out != r {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestConnectionErrorsFoldsActivePlayerAndNotifies(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedGame(t, b)
	conn, id := actorConn(t, store)
	other := "c-bob"
	if id == "bob" {
		other = "c-alice"
	}
	r := getRoom(t, store, "r1")
	if _, err := handleConnectionErrors(context.Background(), r, []string{conn}); err != nil {
		t.Fatal(err)
	}
	// The dropped player is folded and their departure announced, then the table advances
	msgs := b.gw.sentTo(other)
	var sawFold, sawDisconnect, sawTable bool
	for _, m := range msgs {
		switch m.Type {
		case messaging.MessageTypePlayerDisconnected:
			sawDisconnect = true
		case messaging.MessageTypePlayerAction:
			sawFold = m.PlayerAction.Action.Type == table.Fold
		case messaging.MessageTypeTableState:
			sawTable = true
		}
	}
	if !sawDisconnect || !sawFold || !sawTable {
		t.Fatalf("expected disconnect, fold and table state: %+v", msgs)
	}
}

func TestConnectionErrorsRetriesThenSucceeds(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedGame(t, b)
	r := getRoom(t, store, "r1")
	calls := 0
	useStore(t, &stubStore{
		getRoom: func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
		updateRoom: func(ctx context.Context, rr *room.Room) error {
			calls++
			if calls == 1 {
				return db.ErrVersionConflict
			}
			return store.UpdateRoom(ctx, rr)
		},
	})
	if _, err := handleConnectionErrors(context.Background(), r, []string{"c-bob"}); err != nil {
		t.Fatalf("expected success after one conflict: %v", err)
	}
	if calls != 2 {
		t.Fatalf("update attempts = %d want 2", calls)
	}
}

func TestConnectionErrorsFailures(t *testing.T) {
	t.Run("conflicts exhaust retries", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
			updateRoom: func(context.Context, *room.Room) error { return db.ErrVersionConflict },
		})
		_, err := handleConnectionErrors(context.Background(), getRoom(t, store, "r1"), []string{"c-bob"})
		if err == nil || !strings.Contains(err.Error(), "version conflicts") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("context cancelled while backing off", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		useStore(t, &stubStore{
			getRoom:    func(ctx context.Context, rid string) (*room.Room, error) { return store.GetRoom(ctx, rid) },
			updateRoom: func(context.Context, *room.Room) error { return db.ErrVersionConflict },
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := handleConnectionErrors(ctx, getRoom(t, store, "r1"), []string{"c-bob"})
		if err == nil || !strings.Contains(err.Error(), "context cancelled") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("latest room cannot be fetched", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		useStore(t, &stubStore{
			getRoom: func(ctx context.Context, rid string) (*room.Room, error) {
				return nil, errors.New("boom")
			},
			updateRoom: func(context.Context, *room.Room) error { return db.ErrVersionConflict },
		})
		_, err := handleConnectionErrors(context.Background(), getRoom(t, store, "r1"), []string{"c-bob"})
		if err == nil || !strings.Contains(err.Error(), "latest room state") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("unexpected save failure", func(t *testing.T) {
		b := newFakeBackend(t)
		store := seatedGame(t, b)
		useStore(t, &stubStore{updateRoom: func(context.Context, *room.Room) error { return errors.New("boom") }})
		_, err := handleConnectionErrors(context.Background(), getRoom(t, store, "r1"), []string{"c-bob"})
		if err == nil || !strings.Contains(err.Error(), "error updating room state") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestConnectionErrorsRecursesOnFailedBroadcast(t *testing.T) {
	b := newFakeBackend(t)
	store := seatedTrio(t, b, "alice")
	// Bob drops (not to act). The announcement to carol fails, so the handler must
	// recurse to clean her up too. Alice is untouched.
	setGone(b, "c-carol")
	if _, err := handleConnectionErrors(context.Background(), getRoom(t, store, "r1"), []string{"c-bob"}); err != nil {
		t.Fatalf("handleConnectionErrors: %v", err)
	}
	r := getRoom(t, store, "r1")
	if !r.PlayerMap["bob"].SittingOut || !r.PlayerMap["carol"].SittingOut {
		t.Fatalf("bob and carol should both be sitting out: bob=%+v carol=%+v", r.PlayerMap["bob"], r.PlayerMap["carol"])
	}
	if r.PlayerMap["alice"].SittingOut {
		t.Fatal("alice is still connected and should not be sitting out")
	}
}

// ---- broadcast ----

func TestBroadcastCollectsFailures(t *testing.T) {
	b := newFakeBackend(t)
	r := room.NewRoom("r1", room.RoomOpts{})
	r.PlayerMap["alice"] = &room.Player{Id: "alice", TablePos: 0, ConnectionId: "c-alice"}
	r.PlayerMap["bob"] = &room.Player{Id: "bob", TablePos: 1, ConnectionId: "c-bob"}
	r.PlayerMap["carol"] = &room.Player{Id: "carol", TablePos: 2, ConnectionId: ""}
	r.PlayerMap["carol"].ConnectionId = "c-carol"
	registerConn(t, b, "c-alice", "r1", "alice")
	setGone(b, "c-bob", "c-carol")

	errs := broadcast(context.Background(), r, messaging.ToPlayerMessage{Type: messaging.MessageTypePlayerConnected})
	if len(errs) != 2 {
		t.Fatalf("failures = %v, want both dead connections", errs)
	}
	if msgs := b.gw.sentTo("c-alice"); len(msgs) != 1 {
		t.Fatalf("alice should be sent the broadcast: %+v", msgs)
	}
}

func TestBroadcastTableStateIsPerPlayer(t *testing.T) {
	b := newFakeBackend(t)
	r := room.NewRoom("r1", room.RoomOpts{})
	r.PlayerMap["alice"] = &room.Player{Id: "alice", TablePos: 0, ConnectionId: "c-alice"}
	r.PlayerMap["bob"] = &room.Player{Id: "bob", TablePos: 1, ConnectionId: "c-bob"}
	r.GameTable = table.NewFromState(nil, table.State{
		Status: table.Dealing,
		Seats:  []table.Player{{ID: "alice", Chips: 90}, {ID: "bob", Chips: 80}},
	})
	registerConn(t, b, "c-alice", "r1", "alice")
	registerConn(t, b, "c-bob", "r1", "bob")

	if errs := broadcast(context.Background(), r, messaging.ToPlayerMessage{Type: messaging.MessageTypeTableState}); errs != nil {
		t.Fatalf("unexpected failures: %v", errs)
	}
	if got := b.gw.sentTo("c-bob")[0].PlayerState.ID; got != "bob" {
		t.Fatalf("bob should receive his own state, got %q", got)
	}
}
