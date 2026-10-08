package rooms

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
	"github.com/alcamerone/pocket2s/server/db"
	"github.com/alcamerone/pocket2s/server/messaging"
	"github.com/alcamerone/pocket2s/server/room"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Context struct {
	Rooms     db.RoomStore
	Messenger messaging.Messenger
}

func setHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		reqOrigin := req.Header.Get("Origin")
		if reqOrigin == "" {
			log.Printf("received request with no origin header from %s", req.RemoteAddr)
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		rw.Header().Add("Access-Control-Allow-Origin", reqOrigin)
		rw.Header().Add("Access-Control-Allow-Headers", "Content-Type")
		next.ServeHTTP(rw, req)
	})
}

func AttachRoomRoutes(router chi.Router, ctx *Context) {
	router.Route("/room", func(r chi.Router) {
		r.Use(setHeaders)
		r.Get("/check/{roomId}", ctx.handleRoomCheck)
		r.Post("/create/{roomId}", ctx.handleCreateRoom)
		r.Get("/connect/{roomId}/{playerId}", ctx.handleConnect)
	})
}

func (ctx *Context) handleRoomCheck(rw http.ResponseWriter, req *http.Request) {
	roomId := chi.URLParam(req, "roomId")
	r, err := ctx.Rooms.GetRoom(req.Context(), roomId)
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

func (ctx *Context) handleCreateRoom(rw http.ResponseWriter, req *http.Request) {
	roomId := chi.URLParam(req, "roomId")
	r, err := ctx.Rooms.GetRoom(req.Context(), roomId)
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
		PlayerMap: make(map[string]*room.Player, messaging.MAX_PLAYERS),
		Opts:      opts,
	})
	log.Printf("created room %s", roomId)
	rw.WriteHeader(http.StatusCreated)
}

func (ctx *Context) handleConnect(rw http.ResponseWriter, req *http.Request) {
	roomId := chi.URLParam(req, "roomId")
	r, err := ctx.Rooms.GetRoom(req.Context(), roomId)
	if err != nil {
		log.Printf("error accessing room %s: %s", roomId, err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	if r == nil {
		log.Printf("error: room %s does not exist", roomId)
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	playerId := chi.URLParam(req, "playerId")

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

	c, err := wsUpgrader.Upgrade(rw, req, nil)
	if err != nil {
		log.Printf("error establishing connection: %s", err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Store the new connection
	ctx.Messenger.NewConnection(fmt.Sprintf("%s-%s", r.Id, playerId), c)
	if playerExists {
		p.Connected = true
		p.Ready = false
		p.SittingOut = true
		log.Printf("%s has rejoined", playerId)
	} else {
		tablePos := len(r.PlayerMap)
		p = &room.Player{
			Id:        playerId,
			TablePos:  tablePos,
			Connected: true,
		}
		r.PlayerMap[playerId] = p
		log.Printf("%s has joined", playerId)
	}
	err = c.WriteJSON(messaging.ToPlayerMessage{Type: messaging.MessageTypeHello})
	if err != nil {
		// TODO handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}

	errConns := broadcast(context.Background(), ctx.Messenger, r, messaging.ToPlayerMessage{
		Type:     messaging.MessageTypePlayerConnected,
		PlayerId: playerId,
	})
	if errConns != nil {
		handleConnectionErrors(
			context.Background(),
			ctx.Messenger,
			r,
			errConns)
	}
	go listenForPlayerMessages(p, r, c, ctx.Messenger)
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

		// If the player with the connection error is the currently active player,
		// send a message on their behalf to fold to prevent the game from soft-locking
		if r.GameTable.State().ActiveIdx == p.TablePos {
			r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
				Type:   messaging.MessageTypePlayerAction,
				Action: table.Action{Type: table.Fold},
			})
		}

		// Broadcast player disconnected state
		bErrConns := broadcast(ctx, m, r, messaging.ToPlayerMessage{
			Type:     messaging.MessageTypePlayerDisconnected,
			PlayerId: pId,
		})

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
		msg      messaging.FromPlayerMessage
		err      error
		errConns []string
		connId   = fmt.Sprintf("%s-%s", r.Id, p.Id)
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
			if errors.Is(err, table.ErrIllegalAction) ||
				errors.Is(err, table.ErrInsufficientBet) ||
				errors.Is(err, messaging.ErrInvalidMessageType) {
				// TODO Add different messages for different errors?
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
		if msg.Type == messaging.MessageTypeBuyIn ||
			((msg.Type == messaging.MessageTypeReady || msg.Type == messaging.MessageTypeSitOut) && !r.PlayersAreReady()) {
			// No need to send table state if we are still waiting for players
			continue
		}
		if msg.Type == messaging.MessageTypePlayerAction {
			errConns = broadcast(context.Background(), m, r, messaging.ToPlayerMessage{
				Type:         messaging.MessageTypePlayerAction,
				PlayerAction: messaging.PlayerAction{Action: msg.Action, PlayerId: msg.PlayerId},
			})
			if errConns != nil {
				handleConnectionErrors(context.Background(), m, r, errConns)
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
		errConns = broadcast(context.Background(), m, r, resp)
		if errConns != nil {
			handleConnectionErrors(context.Background(), m, r, errConns)
		}
	}
}

// `broadcast` is a convenience method for sending the same message to multiple
// players, modifying the table state for each player so that they only receive
// the information they should have.
// It returns a list of all connections on which the message failed
// to be sent to allow the calling package to perform any actions necessary on
// a connection failure.
func broadcast(
	ctx context.Context,
	m messaging.Messenger,
	r *room.Room,
	msg messaging.ToPlayerMessage,
) (errConns []string) {
	var (
		connId string
		err    error
	)

	for _, p := range r.PlayerMap {
		if p.Connected {
			if msg.Type == messaging.MessageTypeTableState {
				msg.PlayerState = r.GetPlayerState(p.Id)
				if msg.PlayerState.Chips == 0 && r.GameTable.State().Status == table.Done {
					p.Broke = true
				}
			}
			connId = fmt.Sprintf("%s-%s", r.Id, p.Id)
			err = messaging.SendMessageTo(ctx, m, connId, msg)
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
	}

	return errConns
}
