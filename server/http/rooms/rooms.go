package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/conn"
	"github.com/alcamerone/pocket2s/db"
	"github.com/alcamerone/pocket2s/randSource"
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

func broadcast(
	r *types.Room,
	msg types.ToPlayerMessage,
	conns db.ConnectionStore,
) {
	var err error

	for _, p := range r.PlayerMap.All() {
		if msg.Type == types.MessageTypeTableState {
			msg.PlayerState = getPlayerState(p.Id, r.GameTable)
			if msg.PlayerState.Chips == 0 && r.GameTable.State().Status == table.Done {
				p.Broke = true
			}
		}
		err = retrySend(p.Id, r.Id, msg, conns)
		if err != nil {
			log.Printf(
				"giving up sending state to player %s in room %s due to too many errors",
				p.Id,
				r.Id)
			handlePlayerError(p, err, r, conns)
		}
	}
}

func retrySend(
	pId, rId string,
	msg types.ToPlayerMessage,
	cs db.ConnectionStore,
) error {
	var (
		backoff time.Duration
		err     error
	)
	cId := fmt.Sprintf("%s-%s", rId, pId)
	c, err := cs.GetConnection(cId)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Player has gone away; probably handled elsewhere
			return nil
		}
		return fmt.Errorf("unable to retrieve connection %s: %w", cId, err)
	}

	backoff = 100 * time.Millisecond
	for i := 0; i < 5; i++ {
		if c == nil {
			// Player has gone away; probably handled elsewhere
			return nil
		}

		err = c.WriteJSON(msg) // TODO implement
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
	cs db.ConnectionStore,
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

func handleMessageFromPlayer(
	msg types.FromPlayerMessage,
	player *types.Player,
	r *types.Room,
	cs db.ConnectionStore,
) {
	var (
		state table.State
		err   error
	)
	switch msg.Type {
	case types.MessageTypeReady, types.MessageTypeSitOut:
		isReady := msg.Type == types.MessageTypeReady
		player.Ready = isReady
		player.SittingOut = !isReady
		if r.GameTable != nil {
			pState := getPlayerState(player.Id, r.GameTable)
			if pState.ID == player.Id {
				// Player already seated at table
				r.GameTable.SetPlayerDefaulting(player.Id, !isReady)
			} else {
				r.GameTable.AddPlayer(player.Id, !isReady)
			}
		}
		if isReady {
			log.Printf("%s is ready", player.Id)
		} else {
			log.Printf("%s is sitting out", player.Id)
		}
		if (r.GameTable == nil || r.GameTable.State().Status == table.Done) &&
			r.PlayersAreReady() {
			// START THE GAME ALREADY
			if r.GameTable == nil {
				dealer := hand.NewDealer(
					rand.New(
						randSource.NewConcurrencySafeSource(
							time.Now().UnixNano(),
						),
					),
				)
				r.GameTable = table.New(
					dealer,
					table.Options{
						Buyin:   r.Opts.BuyIn,
						Variant: table.TexasHoldem,
						Stakes: table.Stakes{
							BigBlind:   r.Opts.BigBlind,
							SmallBlind: r.Opts.SmallBlind,
							Ante:       r.Opts.Ante,
						},
						Limit:   table.NoLimit,
						OneShot: true,
					},
					r.GetPlayerIds(),
					r.GetPlayersSittingOut())
				state = r.GameTable.State()
			} else {
				state = r.GameTable.NewRound()
			}
		} else {
			return
		}
	case types.MessageTypeBuyIn:
		if r.GameTable != nil {
			err = r.GameTable.BuyPlayerIn(player.Id)
			if err != nil {
				log.Printf("error buying %s in; not found", player.Id)
			}
			player.Broke = false
			handleMessageFromPlayer(
				types.FromPlayerMessage{Type: types.MessageTypeReady},
				player,
				r,
				cs)
			return
		}
	case types.MessageTypePlayerAction:
		state, err = handleActionByPlayer(
			msg.Action,
			player,
			r,
			cs)
		if err != nil {
			log.Println(err.Error())
			return
		}
	default:
		log.Printf("invalid message type %d", msg.Type)
		return
	}
	tableState := obfuscateTableState(state)
	result := getResult(state)
	broadcast(
		r,
		types.ToPlayerMessage{
			Type:       types.MessageTypeTableState,
			TableState: tableState,
			Result:     result,
		}, cs)
	if result != "" {
		r.ResetPlayersReady()
	}
}

func getResult(tableState table.State) string {
	if tableState.Result.Winners == nil ||
		tableState.Result.Contestants == nil ||
		tableState.Result.TableCards == nil {
		return ""
	}
	if len(tableState.Result.Contestants) == 1 {
		return fmt.Sprintf("%s wins.", tableState.Result.Winners[0].ID)
	}
	resultStr := ""
	winningHands := make([]string, len(tableState.Result.Winners))
	var h *hand.Hand
	for i, winner := range tableState.Result.Winners {
		h = hand.New(append(winner.Cards, tableState.Result.TableCards...))
		winningHands[i] = h.Description()
	}
	if len(winningHands) == 1 {
		resultStr += fmt.Sprintf(
			"%s wins with %s",
			tableState.Result.Winners[0].ID,
			winningHands[0])
		return resultStr
	}
	for _, winner := range tableState.Result.Winners {
		resultStr += winner.ID + ", "
	}
	resultStr += "split the pot with "
	for _, handStr := range winningHands {
		resultStr += handStr + ", "
	}
	resultStr += "respectively."
	return resultStr
}

func handleActionByPlayer(
	a table.Action,
	p *types.Player,
	r *types.Room,
	cs db.ConnectionStore,
) (table.State, error) {
	if p.Id != r.GameTable.Active().ID {
		return table.State{}, fmt.Errorf(
			"ignoring action request %s from player %s as it is not their turn",
			a.Type.String(),
			p.Id)
	}
	state, err := r.GameTable.Act(a)
	if err != nil {
		cId := fmt.Sprintf("%s-%s", r.Id, p.Id)
		pc, cErr := cs.GetConnection(cId)
		if cErr != nil {
			handlePlayerError(p, cErr, r, cs)
		}
		pc.WriteJSON(types.ToPlayerMessage{ // TODO Implement
			Type:        types.MessageTypeIllegalAction,
			TableState:  obfuscateTableState(r.GameTable.State()),
			PlayerState: getPlayerState(p.Id, r.GameTable),
		})
		return table.State{}, fmt.Errorf("%s by player %s", err.Error(), p.Id)
	}
	broadcast(
		r,
		types.ToPlayerMessage{
			Type:         types.MessageTypePlayerAction,
			PlayerAction: types.PlayerAction{Action: a, PlayerId: p.Id},
		},
		cs)
	return state, err
}

func obfuscateTableState(tableState table.State) table.State {
	seats := make([]table.Player, len(tableState.Seats))
	for i, player := range tableState.Seats {
		seats[i] = table.Player{
			ID:    player.ID,
			Chips: player.Chips,
		}
		if tableState.Status != table.Done {
			seats[i].ChipsInPot = player.ChipsInPot
		}
		if player.Folded || player.SittingOut {
			// Send an empty array to signal to the front-end
			// that this player has no cards
			seats[i].Cards = make([]hand.Card, 0)
		} else if tableState.Status == table.Done &&
			len(tableState.Result.Contestants) > 1 &&
			playerIsContesting(player.ID, tableState) {
			seats[i].Cards = player.Cards
		}
	}
	tableState.Seats = seats
	active := table.Player{
		ID:         tableState.Active.ID,
		Chips:      tableState.Active.Chips,
		ChipsInPot: tableState.Active.ChipsInPot,
	}
	tableState.Active = active
	return tableState
}

func playerIsContesting(playerId string, tableState table.State) bool {
	for _, contestant := range tableState.Result.Contestants {
		if contestant.ID == playerId {
			return true
		}
	}
	return false
}

func getPlayerState(playerId string, t *table.Table) table.Player {
	for _, s := range t.Seats() {
		if s.ID == playerId {
			return s
		}
	}
	log.Printf("could not find player %s at table", playerId)
	return table.Player{}
}
