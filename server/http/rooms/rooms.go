package httpRooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/messaging"
	"github.com/alcamerone/pocket2s/room"
	pocket2shttp "github.com/alcamerone/pocket2s/server/http"
	"github.com/gocraft/web"
	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func AddRoomRoutes(router *web.Router) {
	router.Subrouter(pocket2shttp.Context{}, "/room").
		Middleware(pocket2shttp.SetHeaders).
		Get("/check/:roomId", handleRoomCheck).
		Post("/create/:roomId", handleCreateRoom).
		Get("/connect/:roomId/:playerId", handleConnect)
}

func handleRoomCheck(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request) {
	r, err := ctx.Rooms.GetRoom(req.Context(), req.PathParams["roomId"])
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r != nil {
		rw.WriteHeader(http.StatusConflict)
		return
	}
	rw.WriteHeader(http.StatusOK)
}

func handleCreateRoom(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request) {
	roomId := req.PathParams["roomId"]
	r, err := ctx.Rooms.GetRoom(req.Context(), req.PathParams["roomId"])
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r != nil {
		log.Printf("error: a room named %s already exists", r.Id)
		rw.WriteHeader(http.StatusConflict)
		return
	}

	var opts room.RoomOpts
	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		log.Printf("Error reading request body: %s", err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	err = json.Unmarshal(reqBody, &opts)
	if err != nil {
		log.Printf("Error unmarshalling request body: %s", err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	ctx.Rooms.NewRoom(req.Context(), &room.Room{
		Id:        roomId,
		PlayerMap: make(map[string]room.Player, messaging.MAX_PLAYERS),
		Opts:      opts,
	})
	log.Printf("created room %s", roomId)
	rw.WriteHeader(http.StatusCreated)
}

func handleConnect(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request) {
	r, err := ctx.Rooms.GetRoom(req.Context(), req.PathParams["roomId"])
	if err != nil {
		log.Printf("error accessing room %s: %s", req.PathParams["roomId"], err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r == nil {
		log.Printf("error: room %s does not exist", req.PathParams["roomId"])
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	playerId := req.PathParams["playerId"]

	p, playerExists := r.PlayerMap[playerId]
	if playerExists {
		// Get the player's connection.
		// If the connection does not exist, continue to reconnect the player.
		// Otherwise, reject the request.
		cExists, err := ctx.Messenger.ConnectionExists(
			context.Background(),
			fmt.Sprintf("%s-%s", r.Id, playerId))
		if err != nil {
			log.Printf("error determining if connection exists for player %s in room %s: %s",
				r.Id,
				playerId,
				err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		if cExists {
			log.Printf("error: a player named %s is already at the table", playerId)
			rw.WriteHeader(http.StatusConflict)
			return
		}
	}
	tableFull := len(r.PlayerMap) > messaging.MAX_PLAYERS
	if tableFull {
		log.Println("error: the table already has the maximum number of players")
		rw.WriteHeader(http.StatusLocked)
		return
	}

	c, err := wsUpgrader.Upgrade(rw, req.Request, nil)
	if err != nil {
		log.Printf("error establishing connection: %s", err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	if playerExists {
		// Store the new connection
		ctx.Messenger.NewConnection(fmt.Sprintf("%s-%s", r.Id, playerId), c)
		p.Ready = false
		p.SittingOut = true
		log.Printf("%s has rejoined", playerId)
	} else {
		tablePos := len(r.PlayerMap)
		p = room.Player{
			Id:       playerId,
			TablePos: tablePos,
		}
		r.PlayerMap[playerId] = p
		log.Printf("%s has joined", playerId)
	}
	err = c.WriteJSON(messaging.ToPlayerMessage{Type: messaging.MessageTypeHello})
	if err != nil {
		// TODO handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}

	errConns := messaging.Broadcast(
		context.Background(),
		ctx.Messenger,
		messaging.ToPlayerMessage{
			Type:     messaging.MessageTypePlayerConnected,
			PlayerId: playerId,
		},
		getActiveConnections(r),
	)
	if errConns != nil {
		handleConnectionErrors(
			context.Background(),
			ctx.Messenger,
			r,
			errConns)
	}
	go listenForPlayerMessages(&p, r, c, ctx.Messenger)
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

// @blocking
func listenForPlayerMessages(
	p *room.Player,
	r *room.Room,
	pc *websocket.Conn,
	m messaging.Messenger,
) {
	var (
		msg    messaging.FromPlayerMessage
		err    error
		connId = fmt.Sprintf("%s-%s", r.Id, p.Id)
	)
	for {
		// Read message
		err = pc.ReadJSON(&msg) // TODO Implement
		if err != nil {
			if m.IsClosedConnectionError(err) {
				m.HandleConnectionError(connId)
				break
			}
			log.Printf("error receiving message from %s: %s", p.Id, err.Error())
			continue
		}

		// Process player action
		_, err = r.HandleMessageFromPlayer(msg)
		if err != nil {
			if errors.Is(err, table.ErrIllegalAction) || errors.Is(err, table.ErrInsufficientBet) {
				// TODO Add a new message type for the latter case?
				connId := fmt.Sprintf("%s-%s", msg.RoomId, msg.PlayerId)
				err = messaging.SendMessageTo(
					context.Background(),
					m,
					connId,
					messaging.ToPlayerMessage{
						Type:        messaging.MessageTypeIllegalAction,
						TableState:  r.GetObfuscatedTableState(),
						PlayerState: r.GetPlayerState(p.Id),
					})
				if err != nil {
					handleConnectionErrors(context.Background(), m, r, []string{connId})
				}
			}
		}

		// Broadcast new table state if necessary
		resp := messaging.ToPlayerMessage{TableState: r.GetObfuscatedTableState()}
		if msg.Type == messaging.MessageTypePlayerAction {
			resp.Type = messaging.MessageTypePlayerAction
			resp.PlayerAction = messaging.PlayerAction{
				Action:   msg.Action,
				PlayerId: msg.PlayerId,
			}
		} else {
			resp.Type = messaging.MessageTypeTableState
			res := r.GetResultStr()
			if res != "" {
				resp.Result = res
			}
		}
		errConns := messaging.Broadcast(context.Background(), m, resp, getActiveConnections(r))
		if errConns != nil {
			handleConnectionErrors(context.Background(), m, r, errConns)
		}
	}
}
