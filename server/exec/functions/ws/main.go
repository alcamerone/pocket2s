package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/server/db"
	"github.com/alcamerone/pocket2s/server/messaging"
	"github.com/alcamerone/pocket2s/server/room"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

const (
	retryCount       = 3
	baseRetryBackoff = 50 * time.Millisecond
)

var (
	successResponse events.APIGatewayProxyResponse = events.APIGatewayProxyResponse{StatusCode: http.StatusOK}
	errResponse     events.APIGatewayProxyResponse = events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}
	roomStore       db.RoomStore
	messenger       messaging.Messenger
)

func mustEnv(v string) (envVar string) {
	if envVar = os.Getenv(v); envVar == "" {
		log.Fatalf("Error: No value for %s", v)
	}
	return envVar
}

func withVersioningRetries(
	ctx context.Context,
	ev events.APIGatewayWebsocketProxyRequest,
	h func(ctx context.Context, ev events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error),
) (events.APIGatewayProxyResponse, error) {
	var delay time.Duration

	backoff := baseRetryBackoff
	for range retryCount {
		resp, err := h(ctx, ev)
		if errors.Is(err, db.ErrVersionConflict) {
			// In the event of a version conflict, back off and retry
			// Randomise delay up to backoff to add jitter
			delay = rand.N(backoff)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				log.Print("Context cancelled while retrying")
				return events.APIGatewayProxyResponse{StatusCode: http.StatusGatewayTimeout}, nil
			}
			backoff *= 2
		} else {
			return resp, err
		}
	}
	log.Printf("Failed to write room state after %d retries, giving up", retryCount)
	return errResponse, nil
}

func handler(
	ctx context.Context,
	ev events.APIGatewayWebsocketProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	switch ev.RequestContext.RouteKey {
	case "$connect":
		return withVersioningRetries(ctx, ev, handleConnect)
	case "$disconnect":
		return withVersioningRetries(ctx, ev, handleDisconnect)
	default:
		return withVersioningRetries(ctx, ev, handleMessage)
	}
}

func handleConnect(
	ctx context.Context,
	ev events.APIGatewayWebsocketProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	roomId := ev.QueryStringParameters["roomId"]
	if roomId == "" {
		log.Printf("Error: no room ID supplied")
		return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, nil
	}
	playerId := ev.QueryStringParameters["playerId"]
	if playerId == "" {
		log.Printf("Error: no player ID supplied")
		return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, nil
	}
	r, err := roomStore.GetRoom(ctx, roomId)
	if err != nil {
		if errors.Is(err, db.ErrRoomNotFound) {
			// TODO upgrade WS and then send error message
			log.Printf("Error: player %s tried to join room %s, which does not exist", playerId, roomId)
			return events.APIGatewayProxyResponse{StatusCode: http.StatusGone}, nil
		}
		log.Printf("Error accessing room %s: %s", roomId, err.Error())
		return errResponse, err
	}

	p, playerExists := r.PlayerMap[playerId]
	if playerExists {
		// Check if the player is currently connected.
		// If not, set a new connection ID for the player.
		// Otherwise, reject the request.
		if p.ConnectionId != "" {
			// Double-check if the connection is actually still live,
			// just in case a room update was missed
			cExists, err := messenger.ConnectionExists(ctx, p.ConnectionId)
			if err != nil {
				log.Printf("error determining if connection exists for player %s in room %s: %s",
					playerId,
					r.Id,
					err.Error())
				return errResponse, err
			}
			if cExists {
				log.Printf("error: a player named %s is already at the table", playerId)
				// TODO upgrade WS and then send error message
				return events.APIGatewayProxyResponse{StatusCode: http.StatusConflict}, nil
			}
		}
	}
	tableFull := !playerExists && len(r.PlayerMap) >= messaging.MAX_PLAYERS
	if tableFull {
		log.Println("error: the table already has the maximum number of players")
		// TODO upgrade WS and then send error message
		return events.APIGatewayProxyResponse{StatusCode: http.StatusLocked}, nil
	}

	// Store connection record
	err = messenger.NewConnection(
		ctx,
		ev.RequestContext.ConnectionID,
		roomId,
		playerId,
		nil)
	if err != nil {
		log.Printf("Error storing new connection record: %s", err.Error())
		return errResponse, err
	}

	// Update table record with new player information
	if playerExists {
		p.ConnectionId = ev.RequestContext.ConnectionID
		p.Ready = false
		p.SittingOut = true
		log.Printf("%s has rejoined", playerId)
	} else {
		tablePos := len(r.PlayerMap)
		p = &room.Player{
			Id:           playerId,
			TablePos:     tablePos,
			ConnectionId: ev.RequestContext.ConnectionID,
		}
		r.PlayerMap[playerId] = p
		log.Printf("%s has joined", playerId)
	}

	// Update the table record
	err = roomStore.UpdateRoom(ctx, r)
	if err != nil {
		log.Printf("Error persisting room state: %s", err.Error())
		return errResponse, err
	}

	return successResponse, nil
}

func handleDisconnect(
	ctx context.Context,
	ev events.APIGatewayWebsocketProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	roomId, _, err := messenger.GetRoomAndPlayerByConnectionId(ctx, ev.RequestContext.ConnectionID)
	if err != nil {
		if errors.Is(err, messaging.ErrNoRoomAndPlayerForConnectionId) {
			// Connection has likely already been cleaned up
			return successResponse, nil
		}
		log.Printf("Error retrieving room ID while handling $disconnect on %s: %s", ev.RequestContext.ConnectionID, err.Error())
		return errResponse, nil
	}

	r, err := roomStore.GetRoom(ctx, roomId)
	if errors.Is(err, db.ErrRoomNotFound) {
		log.Printf("Room %s no longer exists. Not processing disconnect.", roomId)
		return successResponse, nil
	}
	if err != nil {
		log.Printf("Error retrieving room state while handling disconnect on %s: %s", ev.RequestContext.ConnectionID, err.Error())
		return errResponse, nil
	}

	_, err = handleConnectionErrors(ctx, r, []string{ev.RequestContext.ConnectionID})
	if err != nil {
		log.Printf("Error handling connection errors: %s", err.Error())
		// Not much we can do at this point besides hope a future update corrects the state.
		// TODO add a mechanism to reset the table to the last consistent state?
		return errResponse, nil
	}

	return successResponse, nil
}

func handleMessage(
	ctx context.Context,
	ev events.APIGatewayWebsocketProxyRequest,
) (events.APIGatewayProxyResponse, error) {
	m := messaging.FromPlayerMessage{}
	err := json.Unmarshal([]byte(ev.Body), &m)
	if err != nil {
		log.Printf(
			"Error unmarshalling incoming message. Message: '%s', Error: %s",
			ev.Body,
			err.Error())
		return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, nil
	}

	// Get room and player ID from connection ID
	connId := ev.RequestContext.ConnectionID
	roomId, playerId, err := messenger.GetRoomAndPlayerByConnectionId(ctx, connId)
	if err != nil {
		log.Printf("Error retrieving room and player ID for connection ID %s: %s", connId, err.Error())
		return errResponse, err
	}

	// Ensure room and player ID from connection ID matches request
	if roomId != m.RoomId || m.PlayerId != playerId {
		log.Printf(
			"Room and player ID do not match connection. From connection: RoomId: %s PlayerId: %s. From message: RoomId: %s, PlayerId: %s",
			roomId, playerId, m.RoomId, m.PlayerId)
		return events.APIGatewayProxyResponse{StatusCode: http.StatusForbidden}, nil
	}

	// Get room state
	r, err := roomStore.GetRoom(ctx, roomId)
	if err != nil {
		log.Printf("Error retrieving room %s: %s", roomId, err.Error())
		return errResponse, err
	}

	// If "hello" message, just send current room state and broadcast "player connected" message,
	// no changes needed to table
	if m.Type == messaging.MessageTypeHello {
		err = messaging.SendMessageTo(
			ctx,
			messenger,
			connId,
			messaging.ToPlayerMessage{
				Type:        messaging.MessageTypeHello,
				TableState:  r.GetObfuscatedTableState(),
				PlayerState: r.GetPlayerState(m.PlayerId),
			})
		if err != nil {
			var eErr error
			r, eErr = handleConnectionErrors(ctx, r, []string{connId})
			if eErr != nil {
				log.Printf("Error handling connection errors after error responding to 'hello' message: %s, then %s", err.Error(), eErr.Error())
				// Not much we can do at this point besides hope a future update corrects the state.
				// TODO add a mechanism to reset the table to the last consistent state?
				return errResponse, nil
			}
		}
		errConns := broadcast(ctx, r, messaging.ToPlayerMessage{
			Type:     messaging.MessageTypePlayerConnected,
			PlayerId: playerId,
		})
		if errConns != nil {
			_, err = handleConnectionErrors(ctx, r, errConns)
			if err != nil {
				log.Printf("Error handling connection errors after error(s) broadcasting PlayerConnected message: %s", err.Error())
				// As above, just return an error and hope a future update corrects the state
				return errResponse, nil
			}
		}
		return successResponse, nil
	}

	// Process player action
	_, err = r.HandleMessageFromPlayer(m)
	if err != nil {
		// Only type of error that can be thrown here is some form of illegal action,
		// e.g. not your turn, invalid action, etc.
		// TODO Add different messages for different errors?
		mErr := messaging.SendMessageTo(
			ctx,
			messenger,
			connId,
			messaging.ToPlayerMessage{
				Type:        messaging.MessageTypeIllegalAction,
				TableState:  r.GetObfuscatedTableState(),
				PlayerState: r.GetPlayerState(m.PlayerId),
			})
		if mErr != nil {
			var eErr error
			_, eErr = handleConnectionErrors(ctx, r, []string{connId})
			if eErr != nil {
				log.Printf("Error handling connection errors after error processing illegal action: %s, then %s", mErr.Error(), eErr.Error())
				// Not much we can do at this point besides hope a future update corrects the state.
				// TODO add a mechanism to reset the table to the last consistent state?
				return errResponse, nil
			}
		}
		// Don't need to persist room state or send any other messages, so exit early
		return successResponse, nil
	}

	// Try to persist room state first to ensure it remains consistent with what is broadcast to players
	err = roomStore.UpdateRoom(ctx, r)
	if err != nil {
		log.Printf("Error persisting room state: %s", err.Error())
		return errResponse, err
	}

	// Broadcast new table state if necessary
	if m.Type == messaging.MessageTypeBuyIn ||
		((m.Type == messaging.MessageTypeReady || m.Type == messaging.MessageTypeSitOut) && !r.PlayersAreReady()) {
		// No need to send table state if we are still waiting for players; exit early
		return successResponse, nil
	}

	var errConns []string
	if m.Type == messaging.MessageTypePlayerAction {
		errConns = broadcast(ctx, r, messaging.ToPlayerMessage{
			Type:         messaging.MessageTypePlayerAction,
			PlayerAction: messaging.PlayerAction{Action: m.Action, PlayerId: m.PlayerId},
		})
		if errConns != nil {
			r, err = handleConnectionErrors(ctx, r, errConns)
			if err != nil {
				log.Printf("Error handling connection errors after broadcasting PlayerAction message: %s", err.Error())
				// As above, just return an error and hope a future update corrects the state
				return errResponse, nil
			}
		}
	}
	resp := messaging.ToPlayerMessage{
		Type:       messaging.MessageTypeTableState,
		TableState: r.GetObfuscatedTableState(),
	}
	res := r.GetResultStr()
	if res != "" {
		resp.Result = res
	}
	errConns = broadcast(ctx, r, resp)
	if errConns != nil {
		r, err = handleConnectionErrors(ctx, r, errConns)
		if err != nil {
			log.Printf("Error handling connection errors after broadcasting TableState message: %s", err.Error())
			// As above, just return an error and hope a future update corrects the state
			return errResponse, nil
		}
	}

	return successResponse, nil
}

func handleConnectionErrors(
	ctx context.Context,
	r *room.Room,
	errConns []string,
) (*room.Room, error) {
	type tidiedConn struct {
		connId, playerId string
		folded           bool
	}
	var err error
	var handledConns []tidiedConn
	backoff := baseRetryBackoff
	for retry := range retryCount {
		handledConns = handledConns[:0]
		for _, connId := range errConns {
			p := r.GetPlayerByConnectionId(connId)
			if p == nil {
				// Supplied connection ID is stale, ignore and continue
				continue
			}
			log.Printf("Connection %s closed", connId)
			log.Printf("%s is sitting out pending reconnection", p.Id)

			// Mark player disconnected
			p.ConnectionId = ""
			p.SittingOut = true
			r.GameTable.SetPlayerDefaulting(p.Id, true)

			// If the player with the connection error is the currently active player,
			// send a message on their behalf to fold to prevent the game from soft-locking
			folded := false
			if r.GameTable.State().ActiveIdx == p.TablePos {
				_, fErr := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
					PlayerId: p.Id,
					Type:     messaging.MessageTypePlayerAction,
					Action:   table.Action{Type: table.Fold},
				})
				// Ignore error case; if the fold is an illegal action, it shouldn't have happened,
				// i.e. system is working as it should
				if fErr == nil {
					folded = true
				}
			}

			handledConns = append(handledConns, tidiedConn{connId, p.Id, folded})
		}
		if len(handledConns) == 0 {
			// Connections already removed from room, nothing else to do
			return r, nil
		}

		err = roomStore.UpdateRoom(ctx, r)
		if err == nil {
			break
		}
		if errors.Is(err, db.ErrVersionConflict) {
			if retry == retryCount-1 {
				// Exhausted retries, return error
				return r, errors.New("failed to update room state due to version conflicts")
			}

			// Back off, then fetch the latest version of the room and try again
			select {
			case <-ctx.Done():
				return r, errors.New("context cancelled while handling connection errors")
			case <-time.After(rand.N(backoff)):
			}
			backoff *= 2

			var getErr error
			r, getErr = roomStore.GetRoom(ctx, r.Id)
			if getErr != nil {
				return r, fmt.Errorf(
					"error retrieving latest room state while attempting to handle connection errors: %w",
					getErr)
			}
		} else {
			return r, fmt.Errorf("error updating room state: %w", err)
		}
	}
	var newErrConns []string
	for _, hc := range handledConns {
		// Clean up connection
		messenger.HandleConnectionError(ctx, hc.connId)

		// Broadcast player disconnected state
		bErrConns := broadcast(ctx, r, messaging.ToPlayerMessage{
			Type:     messaging.MessageTypePlayerDisconnected,
			PlayerId: hc.playerId,
		})

		// If the active player folded, broadcast this, as well as table state to advance
		// the hand if necessary
		if hc.folded {
			bErrConns = append(bErrConns, broadcast(ctx, r, messaging.ToPlayerMessage{
				Type: messaging.MessageTypePlayerAction,
				PlayerAction: messaging.PlayerAction{
					Action:   table.Action{Type: table.Fold},
					PlayerId: hc.playerId,
				},
			})...)

			resp := messaging.ToPlayerMessage{
				Type:       messaging.MessageTypeTableState,
				TableState: r.GetObfuscatedTableState(),
			}
			res := r.GetResultStr()
			if res != "" {
				resp.Result = res
			}
			bErrConns = append(bErrConns, broadcast(ctx, r, resp)...)
		}

		// Collect any connection IDs on which the broadcast(s) failed
		if len(bErrConns) > 0 {
			for _, connId := range bErrConns {
				if !slices.Contains(newErrConns, connId) {
					newErrConns = append(newErrConns, connId)
				}
			}
		}
	}

	// Recursively handle any errors encountered while broadcasting
	if len(newErrConns) > 0 {
		return handleConnectionErrors(ctx, r, newErrConns)
	}
	return r, nil
}

// `broadcast` is a convenience method for sending the same message to multiple
// players, modifying the table state for each player so that they only receive
// the information they should have.
// It returns a list of all connections on which the message failed
// to be sent to allow the calling package to perform any actions necessary on
// a connection failure.
func broadcast(
	ctx context.Context,
	r *room.Room,
	msg messaging.ToPlayerMessage,
) (errConns []string) {
	var err error

	for _, p := range r.PlayerMap {
		if p.ConnectionId != "" {
			if msg.Type == messaging.MessageTypeTableState {
				msg.PlayerState = r.GetPlayerState(p.Id)
			}
			err = messaging.SendMessageTo(ctx, messenger, p.ConnectionId, msg)
			if err != nil {
				log.Printf(
					"giving up sending state to connection %s due to too many errors",
					p.ConnectionId)
				if errConns == nil {
					errConns = []string{p.ConnectionId}
				} else {
					errConns = append(errConns, p.ConnectionId)
				}
			}
		}
	}

	return errConns
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Instantiate room store
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Error initialising AWS context: %s", err.Error())
	}
	ddbClient := dynamodb.NewFromConfig(cfg)
	roomStore = db.NewDDBRoomStore(ddbClient, mustEnv("DDB_ROOM_STATE_TABLE"))

	// Initialise messenger
	apigwmapi := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(mustEnv("AWS_APIGW_WS_ENDPOINT"))
	})
	messenger = messaging.NewLambdaMessenger(apigwmapi, ddbClient, mustEnv("DDB_CONN_TO_ROOM_PLAYER_TABLE"))

	lambda.Start(handler)
}
