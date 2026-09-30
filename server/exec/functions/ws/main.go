package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/db"
	"github.com/alcamerone/pocket2s/messaging"
	"github.com/alcamerone/pocket2s/room"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func handleMessage(msg json.RawMessage) (any, error) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	m := messaging.FromPlayerMessage{}
	err := json.Unmarshal(msg, &m)
	if err != nil {
		log.Printf(
			"Error unmarshalling incoming message. Message: '%s', Error: %s",
			string(msg),
			err.Error())
		return nil, err
	}

	// Instantiate room store
	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		log.Printf("Error initialising AWS context: %s", err.Error())
		return nil, err
	}
	ddbClient := dynamodb.NewFromConfig(cfg)
	roomStore := db.NewDDBRoomStore(ddbClient, os.Getenv("DDB_ROOM_STATE_TABLE"))

	// Get room state
	r, err := roomStore.GetRoom(context.Background(), m.RoomId)
	if err != nil {
		log.Printf("Error retrieving room %s: %s", m.RoomId, err.Error())
		return nil, err
	}

	// Initialise messenger
	apigwmapi := apigatewaymanagementapi.New(apigatewaymanagementapi.Options{
		BaseEndpoint: aws.String(os.Getenv("AWS_APIGW_WS_ENDPOINT")),
	})
	msgr := messaging.NewLambdaMessenger(apigwmapi)

	// Process player action
	_, err = r.HandleMessageFromPlayer(m)
	if err != nil {
		if errors.Is(err, table.ErrIllegalAction) || errors.Is(err, table.ErrInsufficientBet) {
			// TODO Add a new message type for the latter case?
			connId := fmt.Sprintf("%s-%s", m.RoomId, m.PlayerId)
			err = messaging.SendMessageTo(
				context.Background(),
				msgr,
				connId,
				messaging.ToPlayerMessage{
					Type:        messaging.MessageTypeIllegalAction,
					TableState:  r.GetObfuscatedTableState(),
					PlayerState: r.GetPlayerState(m.PlayerId),
				})
			if err != nil {
				handleConnectionErrors(context.Background(), msgr, r, []string{connId})
			}
		}
	}

	// Try to persist room state to ensure it remains consistent with what is broadcast to players
	err = roomStore.UpdateRoom(context.Background(), r)
	if err != nil {
		log.Printf("Error persisting room state: %s", err.Error())
		return nil, err
	}

	// Broadcast new table state if necessary
	resp := messaging.ToPlayerMessage{TableState: r.GetObfuscatedTableState()}
	if m.Type == messaging.MessageTypePlayerAction {
		resp.Type = messaging.MessageTypePlayerAction
		resp.PlayerAction = messaging.PlayerAction{
			Action:   m.Action,
			PlayerId: m.PlayerId,
		}
	} else {
		resp.Type = messaging.MessageTypeTableState
		res := r.GetResultStr()
		if res != "" {
			resp.Result = res
		}
	}
	errConns := messaging.Broadcast(context.Background(), msgr, resp, getActiveConnections(r))
	if errConns != nil {
		handleConnectionErrors(context.Background(), msgr, r, errConns)
	}

	// Persist room state again in case any updates are made in response to connection errors
	err = roomStore.UpdateRoom(context.Background(), r)
	if err != nil {
		log.Printf("Error persisting room state after handling connection errors: %s", err.Error())
	}

	return nil, nil
}

func getActiveConnections(r *room.Room) []string {
	connIds := make([]string, 0, len(r.PlayerMap))
	for _, p := range r.PlayerMap {
		if p.Connected {
			connIds = append(connIds, fmt.Sprintf("%s-%s", r.Id, p.Id))
		}
	}
	return connIds
}

func handleConnectionErrors(
	ctx context.Context,
	m messaging.Messenger,
	r *room.Room,
	errConns []string,
) {
	for _, connId := range errConns {
		ids := strings.Split(connId, "-")
		pId := ids[1]
		log.Printf("Connection %s closed", connId)
		log.Printf("%s is sitting out pending reconnection", pId)

		// Mark player disconnected
		p := r.PlayerMap[pId]
		p.Connected = false
		p.SittingOut = true
		r.GameTable.SetPlayerDefaulting(pId, true)
		r.PlayerMap[pId] = p

		// If the player with the connection error is the currently active player,
		// send a message on their behalf to fold to prevent the game from soft-locking
		if r.GameTable.State().ActiveIdx == p.TablePos {
			r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
				Type:   messaging.MessageTypePlayerAction,
				Action: table.Action{Type: table.Fold},
			})
		}

		// Broadcast player disconnected state
		conns := getActiveConnections(r)
		bErrConns := messaging.Broadcast(
			ctx,
			m,
			messaging.ToPlayerMessage{
				Type:     messaging.MessageTypePlayerDisconnected,
				PlayerId: pId,
			},
			conns)

		// Recursively handle any errors encountered while broadcasting
		if bErrConns != nil {
			handleConnectionErrors(ctx, m, r, bErrConns)
		}
	}
}

func main() {
	lambda.Start(handleMessage)
}
