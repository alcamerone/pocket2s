package conn

import (
	"context"
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/gorilla/websocket"
)

type Messenger interface {
	SendMessageTo(connId string, msg any) error
}

type InMemoryMessenger struct {
	conns map[string]*websocket.Conn
}

func NewInMemoryMessenger() *InMemoryMessenger {
	return &InMemoryMessenger{
		conns: make(map[string]*websocket.Conn),
	}
}

func (m *InMemoryMessenger) SendMessageTo(connId string, msg any) error {
	return m.conns[connId].WriteJSON(msg)
}

type LambdaMessenger struct {
	endpoint string
}

func NewLambdaMessenger(endpoint string) *LambdaMessenger {
	return &LambdaMessenger{endpoint}
}

func (m *LambdaMessenger) SendMessageTo(ctx context.Context, connId string, msg any) error {
	jMsg, err := json.Marshal(msg)
	apigmapi := apigatewaymanagementapi.New(apigatewaymanagementapi.Options{
		BaseEndpoint: &m.endpoint,
	})
	input := &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: &connId,
		Data:         jMsg,
	}
	_, err = apigmapi.PostToConnection(ctx, input)
	return err
}

type WebSocket interface {
	SendMessage([]byte) error
	HandleIncomingMessages(func([]byte, error))
	ReadJSON(any) error
	WriteJSON(any) error
	Close() error
}

type RawWebSocket struct {
	conn *websocket.Conn
}

func NewRawWebSocket(c *websocket.Conn) *RawWebSocket {
	return &RawWebSocket{c}
}

func (w *RawWebSocket) SendMessage(data []byte) error {
	return w.conn.WriteMessage(0, data)
}

func (w *RawWebSocket) HandleIncomingMessages(f func([]byte, error)) {
	_, msg, err := w.conn.ReadMessage()
	f(msg, err)
}

func (w *RawWebSocket) Close() error {
	return w.conn.Close()
}

func (w *RawWebSocket) ReadJSON(v any) error {
	return w.conn.ReadJSON(v)
}

func (w *RawWebSocket) WriteJSON(v any) error {
	return w.conn.WriteJSON(v)
}
