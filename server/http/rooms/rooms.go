package httpRooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/conn"
	"github.com/alcamerone/pocket2s/db"
	pocket2shttp "github.com/alcamerone/pocket2s/server/http"
	"github.com/alcamerone/pocket2s/types"
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

	var opts types.RoomOpts
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

	ctx.Rooms.NewRoom(req.Context(), &types.Room{
		Id:        roomId,
		PlayerMap: cmap.New[string, *types.Player](types.MAX_PLAYERS),
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

	tableFull := r.PlayerMap.Len() > types.MAX_PLAYERS
	p, playerExists := r.PlayerMap.Get(playerId)
	if playerExists {
		// Get the player's connection.
		// If the connection does not exist, continue to reconnect the player.
		// Otherwise, reject the request.
		_, err := ctx.Connections.GetConnection(fmt.Sprintf("%s-%s", r.Id, playerId))
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			log.Printf("error retrieving connection for player %s in room %s: %s",
				r.Id,
				playerId,
				err.Error())
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !errors.Is(err, db.ErrNotFound) {
			log.Printf("error: a player named %s is already at the table", playerId)
			rw.WriteHeader(http.StatusConflict)
			return
		}
	}
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

	rawWs := conn.NewRawWebSocket(c)
	if playerExists {
		// Store the new connection
		ctx.Connections.NewConnection(fmt.Sprintf("%s-%s", r.Id, playerId), rawWs)
		p.Ready = false
		p.SittingOut = true
		log.Printf("%s has rejoined", playerId)
	} else {
		tablePos := r.PlayerMap.Len()
		p = &types.Player{
			Id:       playerId,
			TablePos: tablePos,
		}
		r.PlayerMap.Set(playerId, p)
		log.Printf("%s has joined", playerId)
	}
	err = c.WriteJSON(types.ToPlayerMessage{Type: types.MessageTypeHello})
	if err != nil {
		// TODO handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}
	broadcast(
		r,
		types.ToPlayerMessage{
			Type:     types.MessageTypePlayerConnected,
			PlayerId: playerId,
		},
		ctx.Connections)
	go listenForPlayerMessages(p, r, rawWs, ctx.Connections)
}

// @blocking
func listenForPlayerMessages(
	p *types.Player,
	r *types.Room,
	pc conn.WebSocket,
	cs db.ConnectionStore,
) {
	var (
		msg types.FromPlayerMessage
		err error
	)
	for {
		err = pc.ReadJSON(&msg) // TODO Implement
		if err != nil {
			if isClosedConnectionError(err.Error()) {
				handlePlayerError(p, err, r, cs)
				break
			}
			log.Printf("error receiving message from %s: %s", p.Id, err.Error())
			continue
		}
		handleMessageFromPlayer(msg, p, r, cs)
	}
}
