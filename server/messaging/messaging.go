package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi/types"
	"github.com/gorilla/websocket"
)

type Messenger interface {
	ConnectionExists(ctx context.Context, id string) (bool, error)
	NewConnection(id string, conn *websocket.Conn)

	// Depending on the type of Messenger, the error that indicates a connection is broken
	// may vary.
	// `IsClosedConnectionError` provides an abstraction that lets each Messenger define
	// this individually.
	IsClosedConnectionError(err error) bool

	// `HandleConnectionError` allows each Messenger type to define any actions that
	// need to be taken to clean up after a broken connection.
	HandleConnectionError(connId string)

	// `sendMessageTo` defines the logic for the actual sending of a message on a
	// connection.
	// Note that this method is only used internally to the `messaging` package;
	// external packages should only use the exposed `SendMessageTo` and `Broadcast`
	// functions.
	sendMessageTo(ctx context.Context, connId string, msg ToPlayerMessage) error
}

type InMemoryMessenger struct {
	conns map[string]*websocket.Conn
}

func NewInMemoryMessenger() *InMemoryMessenger {
	return &InMemoryMessenger{
		conns: make(map[string]*websocket.Conn),
	}
}

func (m *InMemoryMessenger) Connections() map[string]*websocket.Conn {
	return m.conns
}

func (m *InMemoryMessenger) ConnectionExists(_ context.Context, id string) (bool, error) {
	return m.conns[id] != nil, nil
}

func (m *InMemoryMessenger) NewConnection(id string, conn *websocket.Conn) {
	m.conns[id] = conn
}

func (m *InMemoryMessenger) IsClosedConnectionError(err error) bool {
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "unexpected eof") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}

func (m *InMemoryMessenger) HandleConnectionError(connId string) {
	c := m.conns[connId]
	if c != nil {
		c.Close()
	}
	delete(m.conns, connId)
}

func (m *InMemoryMessenger) sendMessageTo(
	ctx context.Context,
	connId string,
	msg ToPlayerMessage,
) error {
	return m.conns[connId].WriteJSON(msg)
}

type LambdaMessenger struct {
	apigwmapiClient *apigatewaymanagementapi.Client
}

func NewLambdaMessenger(apigwmapiClient *apigatewaymanagementapi.Client) *LambdaMessenger {
	return &LambdaMessenger{apigwmapiClient}
}

func (m *LambdaMessenger) ConnectionExists(ctx context.Context, id string) (bool, error) {
	_, err := m.apigwmapiClient.GetConnection(ctx, &apigatewaymanagementapi.GetConnectionInput{
		ConnectionId: &id,
	})
	if err == nil {
		return true, nil
	}
	_, isGoneError := errors.AsType[*types.GoneException](err)
	if isGoneError {
		return false, nil
	}
	return false, err
}

func (m *LambdaMessenger) NewConnection(_ string, _ *websocket.Conn) {
	// No-op; LambdaMessenger does not need to manage connections
}

func (m *LambdaMessenger) IsClosedConnectionError(err error) bool {
	var gone *types.GoneException
	return errors.As(err, &gone)
}

func (m *LambdaMessenger) HandleConnectionError(connId string) {
	// No-op; LambdaMessenger does not need to manage connections
}

func (m *LambdaMessenger) sendMessageTo(
	ctx context.Context,
	connId string,
	msg ToPlayerMessage,
) error {
	jMsg, err := json.Marshal(msg)
	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: &connId,
		Data:         jMsg,
	}
	_, err = m.apigwmapiClient.PostToConnection(ctx, input)
	return err
}

// `Broadcast` is a convenience method for sending the same message on multiple
// connections. It returns a list of all connections on which the message failed
// to be sent to allow the calling package to perform any actions necessary on
// a connection failure.
func Broadcast(
	ctx context.Context,
	m Messenger,
	msg ToPlayerMessage,
	connIds []string,
) (errConns []string) {
	var err error

	for _, connId := range connIds {
		err = SendMessageTo(ctx, m, connId, msg)
		if err != nil {
			log.Printf(
				"giving up sending state to connection %s due to too many errors",
				connId)
			m.HandleConnectionError(connId)
			if errConns == nil {
				errConns = []string{connId}
			} else {
				errConns = append(errConns, connId)
			}
		}
	}

	return errConns
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
			m.HandleConnectionError(connId)
			return err
		}
		log.Printf("error sending message to connection %s: %s", connId, err.Error())
		time.Sleep(backoff)
		backoff *= 2
	}
	// Failed to send message to player after all retries
	m.HandleConnectionError(connId)
	return err
}
