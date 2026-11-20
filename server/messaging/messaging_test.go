package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gorilla/websocket"
)

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

// ---- InMemoryMessenger ----

func TestInMemoryMessengerLifecycle(t *testing.T) {
	ctx := context.Background()
	m := NewInMemoryMessenger()
	serverConn, _ := wsPair(t)

	if ok, err := m.ConnectionExists(ctx, "c1"); ok || err != nil {
		t.Fatalf("ConnectionExists before add = %v, %v", ok, err)
	}
	if err := m.NewConnection(ctx, "c1", "room1", "alice", serverConn); err != nil {
		t.Fatalf("NewConnection: %v", err)
	}
	if ok, _ := m.ConnectionExists(ctx, "c1"); !ok {
		t.Fatal("expected connection to exist")
	}
	if len(m.Connections()) != 1 {
		t.Fatalf("Connections() len = %d", len(m.Connections()))
	}
	if !m.ConnectionExistsForRoom("room1") || m.ConnectionExistsForRoom("room2") {
		t.Fatal("ConnectionExistsForRoom returned wrong result")
	}

	roomID, playerID, err := m.GetRoomAndPlayerByConnectionId(ctx, "c1")
	if err != nil || roomID != "room1" || playerID != "alice" {
		t.Fatalf("GetRoomAndPlayerByConnectionId = %q %q %v", roomID, playerID, err)
	}
	if _, _, err := m.GetRoomAndPlayerByConnectionId(ctx, "nope"); !errors.Is(err, ErrNoRoomAndPlayerForConnectionId) {
		t.Fatalf("unknown connection err = %v", err)
	}

	m.HandleConnectionError(ctx, "c1")
	if ok, _ := m.ConnectionExists(ctx, "c1"); ok {
		t.Fatal("connection should be removed after HandleConnectionError")
	}
	if m.ConnectionExistsForRoom("room1") {
		t.Fatal("room mapping should be removed")
	}
	// Handling an unknown connection is a no-op
	m.HandleConnectionError(ctx, "never-existed")
}

func TestInMemoryMessengerSend(t *testing.T) {
	ctx := context.Background()
	m := NewInMemoryMessenger()
	serverConn, clientConn := wsPair(t)
	if err := m.NewConnection(ctx, "c1", "r", "p", serverConn); err != nil {
		t.Fatal(err)
	}

	if err := SendMessageTo(ctx, m, "c1", ToPlayerMessage{Type: MessageTypePing, Result: "hi"}); err != nil {
		t.Fatalf("SendMessageTo: %v", err)
	}
	var got ToPlayerMessage
	if err := clientConn.ReadJSON(&got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Type != MessageTypePing || got.Result != "hi" {
		t.Fatalf("received %+v", got)
	}
}

func TestInMemoryMessengerSendUnknownConnection(t *testing.T) {
	ctx := context.Background()
	m := NewInMemoryMessenger()
	err := m.sendMessageTo(ctx, "missing", ToPlayerMessage{})
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("got %v want ErrConnectionNotFound", err)
	}
}

func TestInMemoryIsClosedConnectionError(t *testing.T) {
	m := NewInMemoryMessenger()
	if m.IsClosedConnectionError(ErrConnectionNotFound) {
		t.Error("an untracked connection has already been cleaned up, so it is not a closed-connection error")
	}
	if !m.IsClosedConnectionError(&websocket.CloseError{Code: websocket.CloseNormalClosure}) {
		t.Error("a normal close frame should count as closed")
	}
	closed := []string{
		"write: use of closed network connection",
		"write: broken pipe",
		"unexpected EOF",
		"websocket: close 1001 (going away)",
		"read: connection reset by peer",
	}
	for _, s := range closed {
		if !m.IsClosedConnectionError(errors.New(s)) {
			t.Errorf("%q should be a closed-connection error", s)
		}
	}
	if m.IsClosedConnectionError(errors.New("some other failure")) {
		t.Error("unrelated error misclassified as closed connection")
	}
}

// ---- SendMessageTo retry logic, driven by a scripted Messenger ----

var errClosed = errors.New("use of closed network connection")

type scriptedMessenger struct {
	errs    []error
	sent    int
	handled []string
}

func (s *scriptedMessenger) ConnectionExists(context.Context, string) (bool, error) {
	return false, nil
}

func (s *scriptedMessenger) NewConnection(context.Context, string, string, string, *websocket.Conn) error {
	return nil
}

func (s *scriptedMessenger) GetRoomAndPlayerByConnectionId(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (s *scriptedMessenger) IsClosedConnectionError(err error) bool {
	return errors.Is(err, errClosed)
}

func (s *scriptedMessenger) HandleConnectionError(_ context.Context, connId string) {
	s.handled = append(s.handled, connId)
}

func (s *scriptedMessenger) sendMessageTo(_ context.Context, _ string, _ ToPlayerMessage) error {
	i := s.sent
	s.sent++
	if i < len(s.errs) {
		return s.errs[i]
	}
	return nil
}

var _ Messenger = (*scriptedMessenger)(nil)

// recordSleeps swaps the after seam for one whose timers fire immediately and
// returns the recorded backoff durations.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var durations []time.Duration
	orig := after
	after = func(d time.Duration) <-chan time.Time {
		durations = append(durations, d)
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	t.Cleanup(func() { after = orig })
	return &durations
}

func TestSendMessageToSucceedsFirstTry(t *testing.T) {
	sleeps := recordSleeps(t)
	m := &scriptedMessenger{}
	if err := SendMessageTo(context.Background(), m, "c", ToPlayerMessage{}); err != nil {
		t.Fatal(err)
	}
	if m.sent != 1 || len(m.handled) != 0 || len(*sleeps) != 0 {
		t.Fatalf("sent=%d handled=%v sleeps=%v", m.sent, m.handled, *sleeps)
	}
}

func TestSendMessageToRetriesWithBackoff(t *testing.T) {
	sleeps := recordSleeps(t)
	transient := errors.New("transient")
	m := &scriptedMessenger{errs: []error{transient, transient}}
	if err := SendMessageTo(context.Background(), m, "c", ToPlayerMessage{}); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if m.sent != 3 {
		t.Fatalf("sent %d times, want 3", m.sent)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(*sleeps) != 2 || (*sleeps)[0] != want[0] || (*sleeps)[1] != want[1] {
		t.Fatalf("backoffs = %v want %v", *sleeps, want)
	}
	if len(m.handled) != 0 {
		t.Fatalf("should not clean up on eventual success: %v", m.handled)
	}
}

func TestSendMessageToClosedConnectionStopsImmediately(t *testing.T) {
	recordSleeps(t)
	m := &scriptedMessenger{errs: []error{errClosed}}
	err := SendMessageTo(context.Background(), m, "conn-1", ToPlayerMessage{})
	if !errors.Is(err, errClosed) {
		t.Fatalf("got %v", err)
	}
	if m.sent != 1 {
		t.Fatalf("closed connection retried %d times", m.sent)
	}
	if len(m.handled) != 1 || m.handled[0] != "conn-1" {
		t.Fatalf("handled = %v", m.handled)
	}
}

func TestSendMessageToNotFoundStopsWithoutCleanup(t *testing.T) {
	sleeps := recordSleeps(t)
	m := &scriptedMessenger{errs: []error{ErrConnectionNotFound}}
	err := SendMessageTo(context.Background(), m, "gone", ToPlayerMessage{})
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("got %v", err)
	}
	if m.sent != 1 || len(*sleeps) != 0 || len(m.handled) != 0 {
		t.Fatalf("sent=%d sleeps=%v handled=%v", m.sent, *sleeps, m.handled)
	}
}

func TestSendMessageToStopsWhenContextCancelledDuringBackoff(t *testing.T) {
	orig := after
	t.Cleanup(func() { after = orig })
	ctx, cancel := context.WithCancel(context.Background())
	// The timer never fires; cancelling the context is the only way out of the backoff.
	after = func(time.Duration) <-chan time.Time {
		cancel()
		return nil
	}
	m := &scriptedMessenger{errs: []error{errors.New("transient"), errors.New("transient")}}
	err := SendMessageTo(ctx, m, "c", ToPlayerMessage{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want context.Canceled", err)
	}
	if m.sent != 1 {
		t.Fatalf("kept retrying after cancel: sent=%d", m.sent)
	}
	if len(m.handled) != 0 {
		t.Fatalf("cancellation must not clean up the connection: %v", m.handled)
	}
}

func TestSendMessageToGivesUpAfterFiveAttempts(t *testing.T) {
	recordSleeps(t)
	fail := errors.New("persistent")
	m := &scriptedMessenger{errs: []error{fail, fail, fail, fail, fail}}
	err := SendMessageTo(context.Background(), m, "conn-2", ToPlayerMessage{})
	if !errors.Is(err, fail) {
		t.Fatalf("got %v", err)
	}
	if m.sent != 5 {
		t.Fatalf("attempts = %d want 5", m.sent)
	}
	if len(m.handled) != 1 || m.handled[0] != "conn-2" {
		t.Fatalf("handled = %v", m.handled)
	}
}

// ---- LambdaMessenger, against mocked AWS clients ----

// mockAPIGW is a function-field fake of apigwAPI.
type mockAPIGW struct {
	getConnection    func(*apigatewaymanagementapi.GetConnectionInput) error
	postToConnection func(*apigatewaymanagementapi.PostToConnectionInput) error

	gotGet  *apigatewaymanagementapi.GetConnectionInput
	gotPost *apigatewaymanagementapi.PostToConnectionInput
}

func (m *mockAPIGW) GetConnection(_ context.Context, in *apigatewaymanagementapi.GetConnectionInput, _ ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.GetConnectionOutput, error) {
	m.gotGet = in
	return &apigatewaymanagementapi.GetConnectionOutput{}, m.getConnection(in)
}

func (m *mockAPIGW) PostToConnection(_ context.Context, in *apigatewaymanagementapi.PostToConnectionInput, _ ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.PostToConnectionOutput, error) {
	m.gotPost = in
	return &apigatewaymanagementapi.PostToConnectionOutput{}, m.postToConnection(in)
}

// mockDDB is a function-field fake of ddbAPI. Unset functions succeed with empty output.
type mockDDB struct {
	getItem    func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	putItem    func(*dynamodb.PutItemInput) error
	deleteItem func(*dynamodb.DeleteItemInput) error

	gotGet    *dynamodb.GetItemInput
	gotPut    *dynamodb.PutItemInput
	gotDelete *dynamodb.DeleteItemInput
}

func (m *mockDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	m.gotGet = in
	if m.getItem == nil {
		return &dynamodb.GetItemOutput{}, nil
	}
	return m.getItem(in)
}

func (m *mockDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.gotPut = in
	if m.putItem == nil {
		return &dynamodb.PutItemOutput{}, nil
	}
	return &dynamodb.PutItemOutput{}, m.putItem(in)
}

func (m *mockDDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	m.gotDelete = in
	if m.deleteItem == nil {
		return &dynamodb.DeleteItemOutput{}, nil
	}
	return &dynamodb.DeleteItemOutput{}, m.deleteItem(in)
}

func returnErr[T any](err error) func(T) error { return func(T) error { return err } }

func newLambdaMessenger(gw *mockAPIGW, ddb *mockDDB) *LambdaMessenger {
	return NewLambdaMessenger(gw, ddb, "conns")
}

func TestLambdaConnectionExists(t *testing.T) {
	ctx := context.Background()

	t.Run("exists", func(t *testing.T) {
		gw := &mockAPIGW{getConnection: returnErr[*apigatewaymanagementapi.GetConnectionInput](nil)}
		ok, err := newLambdaMessenger(gw, &mockDDB{}).ConnectionExists(ctx, "abc")
		if err != nil || !ok {
			t.Fatalf("got %v, %v", ok, err)
		}
		if *gw.gotGet.ConnectionId != "abc" {
			t.Fatalf("wrong connection id: %q", *gw.gotGet.ConnectionId)
		}
	})

	t.Run("gone", func(t *testing.T) {
		gw := &mockAPIGW{getConnection: returnErr[*apigatewaymanagementapi.GetConnectionInput](&types.GoneException{})}
		ok, err := newLambdaMessenger(gw, &mockDDB{}).ConnectionExists(ctx, "abc")
		if err != nil || ok {
			t.Fatalf("got %v, %v", ok, err)
		}
	})

	t.Run("other error", func(t *testing.T) {
		boom := errors.New("boom")
		gw := &mockAPIGW{getConnection: returnErr[*apigatewaymanagementapi.GetConnectionInput](boom)}
		ok, err := newLambdaMessenger(gw, &mockDDB{}).ConnectionExists(ctx, "abc")
		if !errors.Is(err, boom) || ok {
			t.Fatalf("got %v, %v", ok, err)
		}
	})
}

func TestLambdaNewConnection(t *testing.T) {
	ctx := context.Background()

	t.Run("stores mapping", func(t *testing.T) {
		ddb := &mockDDB{}
		if err := newLambdaMessenger(&mockAPIGW{}, ddb).NewConnection(ctx, "c1", "r1", "p1", nil); err != nil {
			t.Fatalf("NewConnection: %v", err)
		}
		if *ddb.gotPut.TableName != "conns" {
			t.Fatalf("wrong table %q", *ddb.gotPut.TableName)
		}
		for attr, want := range map[string]string{"ConnectionId": "c1", "RoomId": "r1", "PlayerId": "p1"} {
			got, ok := ddb.gotPut.Item[attr].(*ddbTypes.AttributeValueMemberS)
			if !ok || got.Value != want {
				t.Fatalf("item[%s] = %+v want %q", attr, ddb.gotPut.Item[attr], want)
			}
		}
		if _, ok := ddb.gotPut.Item["ExpiresAt"].(*ddbTypes.AttributeValueMemberN); !ok {
			t.Fatalf("item should carry a numeric ExpiresAt: %+v", ddb.gotPut.Item["ExpiresAt"])
		}
	})

	t.Run("put failure", func(t *testing.T) {
		boom := errors.New("boom")
		ddb := &mockDDB{putItem: returnErr[*dynamodb.PutItemInput](boom)}
		if err := newLambdaMessenger(&mockAPIGW{}, ddb).NewConnection(ctx, "c1", "r1", "p1", nil); !errors.Is(err, boom) {
			t.Fatalf("got %v want %v", err, boom)
		}
	})

	t.Run("marshal failure", func(t *testing.T) {
		defer swapMarshal(func(any) (map[string]ddbAV, error) {
			return nil, errors.New("marshal boom")
		})()
		ddb := &mockDDB{}
		if err := newLambdaMessenger(&mockAPIGW{}, ddb).NewConnection(ctx, "c1", "r1", "p1", nil); err == nil {
			t.Fatal("expected marshal error")
		}
		if ddb.gotPut != nil {
			t.Fatal("PutItem must not be called when marshalling fails")
		}
	})
}

func TestLambdaGetRoomAndPlayerByConnectionId(t *testing.T) {
	ctx := context.Background()
	item := func(m map[string]ddbAV) *mockDDB {
		return &mockDDB{getItem: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			return &dynamodb.GetItemOutput{Item: m}, nil
		}}
	}

	t.Run("found", func(t *testing.T) {
		ddb := item(map[string]ddbAV{
			"ConnectionId": &ddbTypes.AttributeValueMemberS{Value: "c1"},
			"RoomId":       &ddbTypes.AttributeValueMemberS{Value: "r1"},
			"PlayerId":     &ddbTypes.AttributeValueMemberS{Value: "p1"},
		})
		roomID, playerID, err := newLambdaMessenger(&mockAPIGW{}, ddb).GetRoomAndPlayerByConnectionId(ctx, "c1")
		if err != nil || roomID != "r1" || playerID != "p1" {
			t.Fatalf("got %q %q %v", roomID, playerID, err)
		}
		key, ok := ddb.gotGet.Key["ConnectionId"].(*ddbTypes.AttributeValueMemberS)
		if !ok || key.Value != "c1" || *ddb.gotGet.TableName != "conns" {
			t.Fatalf("wrong request: %+v", ddb.gotGet)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, _, err := newLambdaMessenger(&mockAPIGW{}, item(nil)).GetRoomAndPlayerByConnectionId(ctx, "c1")
		if !errors.Is(err, ErrNoRoomAndPlayerForConnectionId) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("get failure", func(t *testing.T) {
		boom := errors.New("boom")
		ddb := &mockDDB{getItem: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) { return nil, boom }}
		if _, _, err := newLambdaMessenger(&mockAPIGW{}, ddb).GetRoomAndPlayerByConnectionId(ctx, "c1"); !errors.Is(err, boom) {
			t.Fatalf("got %v want %v", err, boom)
		}
	})

	t.Run("corrupt item", func(t *testing.T) {
		ddb := item(map[string]ddbAV{"ExpiresAt": &ddbTypes.AttributeValueMemberS{Value: "not-a-number"}})
		if _, _, err := newLambdaMessenger(&mockAPIGW{}, ddb).GetRoomAndPlayerByConnectionId(ctx, "c1"); err == nil {
			t.Fatal("expected unmarshal error")
		}
	})
}

func TestLambdaIsClosedConnectionError(t *testing.T) {
	m := &LambdaMessenger{}
	if !m.IsClosedConnectionError(&types.GoneException{}) {
		t.Fatal("GoneException should be a closed-connection error")
	}
	if m.IsClosedConnectionError(errors.New("other")) {
		t.Fatal("unrelated error misclassified")
	}
}

func TestLambdaHandleConnectionError(t *testing.T) {
	// Errors are ignored by design, so a failing delete must not panic or surface.
	ddb := &mockDDB{deleteItem: returnErr[*dynamodb.DeleteItemInput](errors.New("boom"))}
	newLambdaMessenger(&mockAPIGW{}, ddb).HandleConnectionError(context.Background(), "c1")
	if ddb.gotDelete == nil {
		t.Fatal("expected DeleteItem to be called")
	}
	// The table is keyed on ConnectionId, the same attribute the other operations use
	key := ddb.gotDelete.Key
	if got, ok := key["ConnectionId"].(*ddbTypes.AttributeValueMemberS); !ok || got.Value != "c1" || len(key) != 1 {
		t.Fatalf("delete key = %v, want only ConnectionId=c1", key)
	}
	if *ddb.gotDelete.TableName != "conns" {
		t.Fatalf("wrong table %q", *ddb.gotDelete.TableName)
	}
}

func TestLambdaSendMessageTo(t *testing.T) {
	ctx := context.Background()

	t.Run("posts to connection", func(t *testing.T) {
		gw := &mockAPIGW{postToConnection: returnErr[*apigatewaymanagementapi.PostToConnectionInput](nil)}
		if err := newLambdaMessenger(gw, &mockDDB{}).sendMessageTo(ctx, "c1", ToPlayerMessage{Type: MessageTypeHello}); err != nil {
			t.Fatalf("sendMessageTo: %v", err)
		}
		if *gw.gotPost.ConnectionId != "c1" {
			t.Fatalf("wrong connection id %q", *gw.gotPost.ConnectionId)
		}
		var sent ToPlayerMessage
		if err := json.Unmarshal(gw.gotPost.Data, &sent); err != nil || sent.Type != MessageTypeHello {
			t.Fatalf("payload = %s (%v)", gw.gotPost.Data, err)
		}
	})

	t.Run("post failure", func(t *testing.T) {
		boom := errors.New("boom")
		gw := &mockAPIGW{postToConnection: returnErr[*apigatewaymanagementapi.PostToConnectionInput](boom)}
		if err := newLambdaMessenger(gw, &mockDDB{}).sendMessageTo(ctx, "c1", ToPlayerMessage{}); !errors.Is(err, boom) {
			t.Fatalf("got %v want %v", err, boom)
		}
	})

	t.Run("marshal failure", func(t *testing.T) {
		defer swapJSON(func(any) ([]byte, error) { return nil, errors.New("json boom") })()
		gw := &mockAPIGW{}
		if err := newLambdaMessenger(gw, &mockDDB{}).sendMessageTo(ctx, "c1", ToPlayerMessage{}); err == nil {
			t.Fatal("expected marshal error")
		}
		if gw.gotPost != nil {
			t.Fatal("PostToConnection must not be called when marshalling fails")
		}
	})
}

// Ensure the full LambdaMessenger path works through the public SendMessageTo
// wrapper, including the closed-connection cleanup.
func TestLambdaSendMessageToGoneCleansUp(t *testing.T) {
	sleeps := recordSleeps(t)
	gw := &mockAPIGW{postToConnection: returnErr[*apigatewaymanagementapi.PostToConnectionInput](&types.GoneException{})}
	ddb := &mockDDB{}
	err := SendMessageTo(context.Background(), newLambdaMessenger(gw, ddb), "c9", ToPlayerMessage{})
	if err == nil {
		t.Fatal("expected error for gone connection")
	}
	if len(*sleeps) != 0 {
		t.Fatalf("gone connection must not be retried: %v", *sleeps)
	}
	if ddb.gotDelete == nil {
		t.Fatal("expected cleanup delete")
	}
}

// Compile-time checks that the real clients satisfy the interfaces.
var (
	_ apigwAPI = (*apigatewaymanagementapi.Client)(nil)
	_ ddbAPI   = (*dynamodb.Client)(nil)
)
