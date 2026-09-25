package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/room"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/gorilla/websocket"
)

type Messenger interface {
	SendMessageTo(connId string, msg ToPlayerMessage) error
	HandleSendError()
}

type InMemoryMessenger struct {
	conns map[string]*websocket.Conn
}

func NewInMemoryMessenger() *InMemoryMessenger {
	return &InMemoryMessenger{
		conns: make(map[string]*websocket.Conn),
	}
}

func (m *InMemoryMessenger) SendMessageTo(connId string, msg ToPlayerMessage) error {
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

func Broadcast(m Messenger, r *room.Room, msg ToPlayerMessage) {
	var (
		err    error
		connId string
	)

	for _, p := range r.PlayerMap {
		if msg.Type == MessageTypeTableState {
			msg.PlayerState = r.GetPlayerState(p.Id)
			if msg.PlayerState.Chips == 0 && r.GameTable.State().Status == table.Done {
				p.Broke = true
			}
		}
		connId = fmt.Sprintf("%s-%s", r.Id, p.Id)
		err = retrySend(m, connId, msg)
		if err != nil {
			log.Printf(
				"giving up sending state to player %s in room %s due to too many errors",
				p.Id,
				r.Id)
			handlePlayerError(p, err, r)
		}
	}
}

func retrySend(
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
		err = m.SendMessageTo(connId, msg)
		if err == nil {
			return nil
		}
		if isClosedConnectionError(err.Error()) {
			c.Close()
			dErr := cs.DeleteConnection(fmt.Sprintf("%s-%s", rId, pId))
			if dErr != nil {
				// We will keep trying this every time we fail to use the broken connection,
				// so just print the error and continue
				log.Printf("failed to delete connection %s: %s", cId, dErr)
			}
			return err
		}
		log.Printf("error sending state to player %s: %s", pId, err.Error())
		time.Sleep(backoff)
		backoff *= 2
	}
	c.Close()
	dErr := cs.DeleteConnection(fmt.Sprintf("%s-%s", rId, pId))
	if dErr != nil {
		// We will keep trying this every time we fail to use the broken connection,
		// so just print the error and continue
		log.Printf("failed to delete connection %s: %s", cId, dErr)
	}
	return err
}

func isClosedConnectionError(errStr string) bool {
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "Broken pipe") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "unexpected EOF") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}

func handlePlayerError(
	p *types.Player,
	err error,
	r *types.Room,
) {
	cId := fmt.Sprintf("%s-%s", r.Id, p.Id)
	log.Printf("connection %s closed with %s", cId, err.Error())
	log.Printf("%s is sitting out pending reconnection", p.Id)
	dErr := cs.DeleteConnection(cId)
	if dErr != nil {
		// We will keep trying this every time we fail to use the broken connection,
		// so just print the error and continue
		log.Printf("failed to delete connection %s: %s", cId, dErr)
	}
	p.SittingOut = true
	broadcast(
		r,
		types.ToPlayerMessage{
			Type:     types.MessageTypePlayerDisconnected,
			PlayerId: p.Id,
		},
		cs)
	if r.GameTable != nil {
		r.GameTable.SetPlayerDefaulting(p.Id, true)
		if r.GameTable.State().Active.ID == p.Id {
			handleMessageFromPlayer(
				types.FromPlayerMessage{
					Type: types.MessageTypePlayerAction,
					Action: table.Action{
						Type: table.Fold,
					},
				},
				p,
				r,
				cs)
		}
	}
}
