package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
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
	conns                  map[string]*websocket.Conn
	connsToRoomAndPlayerId map[string]roomAndPlayerId
}

func NewInMemoryMessenger() *InMemoryMessenger {
	return &InMemoryMessenger{
		conns:                  make(map[string]*websocket.Conn),
		connsToRoomAndPlayerId: make(map[string]roomAndPlayerId),
	}
}

func (m *InMemoryMessenger) Connections() map[string]*websocket.Conn {
	return m.conns
}

func (m *InMemoryMessenger) ConnectionExists(_ context.Context, id string) (bool, error) {
	return m.conns[id] != nil, nil
}

func (m *InMemoryMessenger) NewConnection(
	_ context.Context,
	connId, roomId, playerId string,
	conn *websocket.Conn,
) error {
	m.conns[connId] = conn
	m.connsToRoomAndPlayerId[connId] = roomAndPlayerId{RoomId: roomId, PlayerId: playerId}
	return nil
}

func (m *InMemoryMessenger) GetRoomAndPlayerByConnectionId(_ context.Context, connId string) (
	roomId, playerId string,
	err error,
) {
	rpId, ok := m.connsToRoomAndPlayerId[connId]
	if !ok {
		return "", "", ErrNoRoomAndPlayerForConnectionId
	}
	return rpId.RoomId, rpId.PlayerId, nil
}

func (m *InMemoryMessenger) ConnectionExistsForRoom(roomId string) bool {
	for _, rp := range m.connsToRoomAndPlayerId {
		if rp.RoomId == roomId {
			return true
		}
	}
	return false
}

func (m *InMemoryMessenger) IsClosedConnectionError(err error) bool {
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "unexpected eof") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}

func (m *InMemoryMessenger) HandleConnectionError(_ context.Context, connId string) {
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
	return m.conns[connId].WriteJSON(msg)
}

type LambdaMessenger struct {
	apigwmapiClient             *apigatewaymanagementapi.Client
	ddbClient                   *dynamodb.Client
	connsToRoomAndPlayerIdTable string
}

func NewLambdaMessenger(
	apigwmapiClient *apigatewaymanagementapi.Client,
	ddbClient *dynamodb.Client,
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
	ddbItem, err := attributevalue.MarshalMap(roomAndPlayerId{
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
	connId, err := attributevalue.Marshal(id)
	if err != nil {
		return "", "", err
	}
	resp, err := m.ddbClient.GetItem(ctx, &dynamodb.GetItemInput{
		Key:       map[string]ddbTypes.AttributeValue{"ConnectionId": connId},
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
	key, err := attributevalue.Marshal(connId)
	if err != nil {
		return
	}

	// Ignore error; stale mapping is not an issue, just attempt to tidy up
	m.ddbClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &m.connsToRoomAndPlayerIdTable,
		Key:       map[string]ddbTypes.AttributeValue{"connectionId": key},
	})
}

func (m *LambdaMessenger) sendMessageTo(
	ctx context.Context,
	connId string,
	msg ToPlayerMessage,
) error {
	jMsg, err := json.Marshal(msg)
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
		log.Printf("error sending message to connection %s: %s", connId, err.Error())
		time.Sleep(backoff)
		backoff *= 2
	}
	// Failed to send message to player after all retries
	m.HandleConnectionError(ctx, connId)
	return err
}
