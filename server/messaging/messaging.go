package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gorilla/websocket"
)

var (
	ErrInvalidMessageType             = errors.New("messaging: Invalid message type")
	ErrNoRoomAndPlayerForConnectionId = errors.New("messaging: No room and player matches the provided connection ID")
	ErrConnectionNotFound             = errors.New("messaging: No connection exists for the provided connection ID")
)

// Seams over external functions whose failure paths cannot otherwise be reached
// in tests. They are plain function values so tests can substitute failures.
var (
	marshalMap  = attributevalue.MarshalMap
	jsonMarshal = json.Marshal
	after       = time.After
)

type roomAndPlayerId struct {
	ConnectionId string
	RoomId       string
	PlayerId     string
	ExpiresAt    int64
}

type Messenger interface {
	ConnectionExists(ctx context.Context, id string) (bool, error)
	NewConnection(
		ctx context.Context,
		connId, roomId, playerId string,
		conn *websocket.Conn,
	) error

	GetRoomAndPlayerByConnectionId(ctx context.Context, connId string) (
		roomId, playerId string,
		err error,
	)

	// Depending on the type of Messenger, the error that indicates a connection is broken
	// may vary.
	// `IsClosedConnectionError` provides an abstraction that lets each Messenger define
	// this individually.
	IsClosedConnectionError(err error) bool

	// `HandleConnectionError` allows each Messenger type to define any actions that
	// need to be taken to clean up after a broken connection.
	HandleConnectionError(ctx context.Context, connId string)

	// `sendMessageTo` defines the logic for the actual sending of a message on a
	// connection.
	// Note that this method is only used internally to the `messaging` package;
	// external packages should only use the exposed `SendMessageTo` and `Broadcast`
	// functions.
	sendMessageTo(ctx context.Context, connId string, msg ToPlayerMessage) error
}

type InMemoryMessenger struct {
	// mu guards both maps: connections are added, removed and read from many goroutines
	mu                     sync.RWMutex
	conns                  map[string]*websocket.Conn
	connsToRoomAndPlayerId map[string]roomAndPlayerId
}

func NewInMemoryMessenger() *InMemoryMessenger {
	return &InMemoryMessenger{
		conns:                  make(map[string]*websocket.Conn),
		connsToRoomAndPlayerId: make(map[string]roomAndPlayerId),
	}
}

// Connections returns a copy of the current connection map.
func (m *InMemoryMessenger) Connections() map[string]*websocket.Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return maps.Clone(m.conns)
}

func (m *InMemoryMessenger) ConnectionExists(_ context.Context, id string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[id] != nil, nil
}

func (m *InMemoryMessenger) NewConnection(
	_ context.Context,
	connId, roomId, playerId string,
	conn *websocket.Conn,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[connId] = conn
	m.connsToRoomAndPlayerId[connId] = roomAndPlayerId{RoomId: roomId, PlayerId: playerId}
	return nil
}

func (m *InMemoryMessenger) GetRoomAndPlayerByConnectionId(_ context.Context, connId string) (
	roomId, playerId string,
	err error,
) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rpId, ok := m.connsToRoomAndPlayerId[connId]
	if !ok {
		return "", "", ErrNoRoomAndPlayerForConnectionId
	}
	return rpId.RoomId, rpId.PlayerId, nil
}

func (m *InMemoryMessenger) ConnectionExistsForRoom(roomId string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rp := range m.connsToRoomAndPlayerId {
		if rp.RoomId == roomId {
			return true
		}
	}
	return false
}

func (m *InMemoryMessenger) IsClosedConnectionError(err error) bool {
	// Any close frame, normal or otherwise, ends the connection. Reading again
	// after such an error panics in the websocket library, so it must not be
	// treated as a transient failure.
	if _, isClose := errors.AsType[*websocket.CloseError](err); isClose {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "unexpected eof") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}

func (m *InMemoryMessenger) HandleConnectionError(_ context.Context, connId string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.conns[connId]
	if c != nil {
		c.Close()
	}
	delete(m.conns, connId)
	delete(m.connsToRoomAndPlayerId, connId)
}

func (m *InMemoryMessenger) sendMessageTo(
	_ context.Context,
	connId string,
	msg ToPlayerMessage,
) error {
	m.mu.RLock()
	c := m.conns[connId]
	m.mu.RUnlock()
	if c == nil {
		return ErrConnectionNotFound
	}
	return c.WriteJSON(msg)
}

// apigwAPI is the subset of the API Gateway management client used by LambdaMessenger.
// *apigatewaymanagementapi.Client satisfies it.
type apigwAPI interface {
	GetConnection(ctx context.Context, in *apigatewaymanagementapi.GetConnectionInput, opts ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.GetConnectionOutput, error)
	PostToConnection(ctx context.Context, in *apigatewaymanagementapi.PostToConnectionInput, opts ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.PostToConnectionOutput, error)
}

// ddbAPI is the subset of the DynamoDB client used by LambdaMessenger.
// *dynamodb.Client satisfies it.
type ddbAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type LambdaMessenger struct {
	apigwmapiClient             apigwAPI
	ddbClient                   ddbAPI
	connsToRoomAndPlayerIdTable string
}

func NewLambdaMessenger(
	apigwmapiClient apigwAPI,
	ddbClient ddbAPI,
	connsToRoomAndPlayerIdTable string,
) *LambdaMessenger {
	return &LambdaMessenger{apigwmapiClient, ddbClient, connsToRoomAndPlayerIdTable}
}

func (m *LambdaMessenger) ConnectionExists(ctx context.Context, id string) (bool, error) {
	_, err := m.apigwmapiClient.GetConnection(ctx, &apigatewaymanagementapi.GetConnectionInput{
		ConnectionId: &id,
	})
	if err == nil {
		return true, nil
	}
	if _, isGoneError := errors.AsType[*types.GoneException](err); isGoneError {
		return false, nil
	}
	return false, err
}

func (m *LambdaMessenger) NewConnection(
	ctx context.Context,
	connId, roomId, playerId string,
	_ *websocket.Conn,
) error {
	// Create entry mapping connection ID to room and player ID
	ddbItem, err := marshalMap(roomAndPlayerId{
		ConnectionId: connId,
		RoomId:       roomId,
		PlayerId:     playerId,
		ExpiresAt:    time.Now().Add(3 * time.Hour).Unix(),
	})
	if err != nil {
		return err
	}
	_, err = m.ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &m.connsToRoomAndPlayerIdTable,
		Item:      ddbItem,
	})
	return err
}

func (m *LambdaMessenger) GetRoomAndPlayerByConnectionId(ctx context.Context, id string) (
	roomId, playerId string,
	err error,
) {
	resp, err := m.ddbClient.GetItem(ctx, &dynamodb.GetItemInput{
		Key:       map[string]ddbTypes.AttributeValue{"ConnectionId": stringKey(id)},
		TableName: &m.connsToRoomAndPlayerIdTable,
	})
	if err != nil {
		return "", "", err
	}
	if resp.Item == nil {
		return "", "", ErrNoRoomAndPlayerForConnectionId
	}

	var rpId roomAndPlayerId
	err = attributevalue.UnmarshalMap(resp.Item, &rpId)
	if err != nil {
		return "", "", err
	}
	return rpId.RoomId, rpId.PlayerId, nil
}

func (m *LambdaMessenger) IsClosedConnectionError(err error) bool {
	var gone *types.GoneException
	return errors.As(err, &gone)
}

func (m *LambdaMessenger) HandleConnectionError(ctx context.Context, connId string) {
	// Remove entry mapping connection ID to room and player ID
	// Ignore error; stale mapping is not an issue, just attempt to tidy up
	m.ddbClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &m.connsToRoomAndPlayerIdTable,
		Key:       map[string]ddbTypes.AttributeValue{"ConnectionId": stringKey(connId)},
	})
}

func (m *LambdaMessenger) sendMessageTo(
	ctx context.Context,
	connId string,
	msg ToPlayerMessage,
) error {
	jMsg, err := jsonMarshal(msg)
	if err != nil {
		return err
	}
	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: &connId,
		Data:         jMsg,
	}
	_, err = m.apigwmapiClient.PostToConnection(ctx, input)
	return err
}

func SendMessageTo(
	ctx context.Context,
	m Messenger,
	connId string,
	msg ToPlayerMessage,
) error {
	var (
		backoff time.Duration
		err     error
	)

	backoff = 100 * time.Millisecond
	for range 5 {
		err = m.sendMessageTo(ctx, connId, msg)
		if err == nil {
			return nil
		}
		if m.IsClosedConnectionError(err) {
			m.HandleConnectionError(ctx, connId)
			return err
		}
		// The connection is already gone and cleaned up, so retrying is pointless
		// and there is nothing left to tidy.
		if errors.Is(err, ErrConnectionNotFound) {
			return err
		}
		log.Printf("error sending message to connection %s: %s", connId, err.Error())
		select {
		case <-after(backoff):
		case <-ctx.Done():
			// Cancelled while backing off; abandon the send without treating
			// the connection as dead.
			return ctx.Err()
		}
		backoff *= 2
	}
	// Failed to send message to player after all retries
	m.HandleConnectionError(ctx, connId)
	return err
}

func stringKey(v string) ddbTypes.AttributeValue {
	return &ddbTypes.AttributeValueMemberS{Value: v}
}
