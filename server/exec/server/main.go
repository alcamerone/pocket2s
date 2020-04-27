/*    package "server/main" defines the Pocket2s server.
 *    Copyright (C) 2020 Cameron Ekblad.
 *    Email: al.camerone@gmail.com
 *
 *    This program is free software: you can redistribute it and/or modify
 *    it under the terms of the GNU Affero General Public License as published
 *    by the Free Software Foundation, either version 3 of the License, or
 *    (at your option) any later version.
 *
 *    This program is distributed in the hope that it will be useful,
 *    but WITHOUT ANY WARRANTY; without even the implied warranty of
 *    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *    GNU Affero General Public License for more details.
 *
 *    You should have received a copy of the GNU Affero General Public License
 *    along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package main

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/randSource"
	"github.com/alcamerone/pocket2s/types"
	"github.com/gocraft/web"
	"github.com/gorilla/websocket"
)

const (
	MAX_PLAYERS         = 6
	DEFAULT_BUY_IN      = 2000
	DEFAULT_BIG_BLIND   = 20
	DEFAULT_SMALL_BLIND = 10
)

type playerMap struct {
	sync.RWMutex
	players map[string]*types.Player
}

type room struct {
	id        string
	playerMap playerMap
	gameTable *table.Table
}

var (
	router   *web.Router
	roomMap  = make(map[string]*room)
	roomLock = sync.RWMutex{}
)

func (r *room) getPlayerIds() []string {
	r.playerMap.RLock()
	defer r.playerMap.RUnlock()
	playerIds := make([]string, len(r.playerMap.players))
	for _, player := range r.playerMap.players {
		playerIds[player.TablePos] = player.Id
	}
	return playerIds
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Context struct{}

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// TODO default room for dev. Remove before prod
	roomMap["pocket2s"] = &room{
		id: "pocket2s",
		playerMap: playerMap{
			players: make(map[string]*types.Player, MAX_PLAYERS),
		},
	}
	router = web.New(Context{})
	router.Post("/create/:roomId", handleCreateRoom).
		Get("/connect/:roomId/:playerId", handleConnect)
}

func main() {
	log.Println("starting server on port 2222")
	err := http.ListenAndServe(":2222", router)
	if err != nil {
		log.Fatal("error in main loop: " + err.Error())
	}
}

func handleCreateRoom(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	roomId := req.PathParams["roomId"]
	roomLock.RLock()
	if room := roomMap[roomId]; room != nil {
		log.Printf("error: a room named %s already exists", roomId)
		rw.WriteHeader(http.StatusConflict)
		roomLock.RUnlock()
		return
	}
	roomLock.RUnlock()

	roomLock.Lock()
	defer roomLock.Unlock()
	roomMap[roomId] = &room{
		id: roomId,
		playerMap: playerMap{
			players: make(map[string]*types.Player, MAX_PLAYERS),
		},
	}
	rw.WriteHeader(http.StatusCreated)
}

func handleConnect(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	roomId := req.PathParams["roomId"]
	var r *room
	roomLock.Lock()
	defer roomLock.Unlock()
	if r = roomMap[roomId]; r == nil {
		log.Printf("error: room %s does not exist", roomId)
		rw.WriteHeader(http.StatusNotFound)
		return
	}

	playerId := req.PathParams["playerId"]

	r.playerMap.Lock()
	tableFull := len(r.playerMap.players) > MAX_PLAYERS
	existingPlayer, playerExists := r.playerMap.players[playerId]
	if playerExists && existingPlayer.Conn != nil {
		log.Printf("error: a player named %s is already at the table")
		rw.WriteHeader(http.StatusConflict)
		return
	}
	if tableFull {
		log.Printf("error: the table already has the maximum number of players")
		rw.WriteHeader(http.StatusLocked)
		return
	}

	conn, err := wsUpgrader.Upgrade(rw, req.Request, nil)
	if err != nil {
		log.Println("error establishing connection: %s", err.Error())
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}

	if playerExists {
		existingPlayer.Conn = conn
		existingPlayer.Ready = false
		existingPlayer.SittingOut = true
		log.Printf("%s has rejoined", playerId)
	} else {
		tablePos := len(r.playerMap.players)
		r.playerMap.players[playerId] = &types.Player{
			Id:       playerId,
			TablePos: tablePos,
			Conn:     conn,
		}
		log.Printf("%s has joined", playerId)
	}
	err = conn.WriteJSON(types.ToPlayerMessage{Type: types.MessageTypeHello})
	if err != nil {
		// TODO handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}
	r.playerMap.Unlock()
	r.broadcast(types.ToPlayerMessage{
		Type:     types.MessageTypePlayerConnected,
		PlayerId: playerId,
	})
	go listenForPlayerMessages(r.playerMap.players[playerId], r)
}

// @blocking
func listenForPlayerMessages(player *types.Player, r *room) {
	var (
		msg types.FromPlayerMessage
		err error
	)
	for {
		err = player.Conn.ReadJSON(&msg)
		if err != nil {
			if isClosedConnectionError(err.Error()) {
				r.handlePlayerError(player, err)
				player.Conn = nil
				break
			}
			log.Printf("error receiving message from %s: %s", player.Id, err.Error())
			continue
		}
		r.handleMessageFromPlayer(msg, player)
	}
}

func (r *room) handleMessageFromPlayer(
	msg types.FromPlayerMessage,
	player *types.Player,
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
		if r.gameTable != nil {
			pState := getPlayerState(player.Id, r.gameTable)
			if pState.ID == player.Id {
				// Player already seated at table
				r.gameTable.SetPlayerDefaulting(player.Id, !isReady)
			} else {
				r.gameTable.AddPlayer(player.Id, !isReady)
			}
		}
		if isReady {
			log.Printf("%s is ready", player.Id)
		} else {
			log.Printf("%s is sitting out", player.Id)
		}
		if (r.gameTable == nil || r.gameTable.State().Status == table.Done) &&
			r.playersAreReady() {
			// START THE GAME ALREADY
			if r.gameTable == nil {
				dealer := hand.NewDealer(
					rand.New(
						randSource.NewConcurrencySafeSource(
							time.Now().UnixNano(),
						),
					),
				)
				r.gameTable = table.New(
					dealer,
					table.Options{
						Buyin:   DEFAULT_BUY_IN,
						Variant: table.TexasHoldem,
						Stakes: table.Stakes{
							BigBlind:   DEFAULT_BIG_BLIND,
							SmallBlind: DEFAULT_SMALL_BLIND,
							Ante:       0,
						},
						Limit:   table.NoLimit,
						OneShot: true,
					},
					r.getPlayerIds(),
					r.getPlayersSittingOut())
				state = r.gameTable.State()
			} else {
				state = r.gameTable.NewRound()
			}
		} else {
			return
		}
	case types.MessageTypeBuyIn:
		if r.gameTable != nil {
			err = r.gameTable.BuyPlayerIn(player.Id)
			if err != nil {
				log.Printf("error buying %s in; not found", player.Id)
			}
			player.Broke = false
			r.handleMessageFromPlayer(
				types.FromPlayerMessage{Type: types.MessageTypeReady},
				player)
			return
		}
	case types.MessageTypePlayerAction:
		state, err = r.handleActionByPlayer(msg.Action, player)
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
	r.broadcast(types.ToPlayerMessage{
		Type:       types.MessageTypeTableState,
		TableState: tableState,
		Result:     result,
	})
	if result != "" {
		r.resetPlayersReady()
	}
	return
}

func (r *room) playersAreReady() bool {
	r.playerMap.RLock()
	defer r.playerMap.RUnlock()
	if len(r.playerMap.players) < 2 {
		return false
	}
	var nSittingOut int
	for _, player := range r.playerMap.players {
		if !player.Ready && !player.SittingOut && !player.Broke {
			return false
		}
		if player.SittingOut || player.Broke {
			nSittingOut++
		}
	}
	if len(r.playerMap.players)-nSittingOut < 2 {
		return false
	}
	return true
}

func (r *room) resetPlayersReady() {
	r.playerMap.RLock()
	defer r.playerMap.RUnlock()
	for id := range r.playerMap.players {
		r.playerMap.players[id].Ready = false
	}
}

func (r *room) getPlayersSittingOut() []string {
	r.playerMap.RLock()
	defer r.playerMap.RUnlock()
	sittingOut := make([]string, 0)
	for _, p := range r.playerMap.players {
		if p.SittingOut {
			sittingOut = append(sittingOut, p.Id)
		}
	}
	return sittingOut
}

func (r *room) handleActionByPlayer(action table.Action, player *types.Player) (table.State, error) {
	if player.Id != r.gameTable.Active().ID {
		return table.State{}, fmt.Errorf(
			"ignoring action request %s from player %s as it is not their turn",
			action.Type.String(),
			player.Id)
	}
	state, err := r.gameTable.Act(action)
	if err != nil {
		// TODO handle error
		player.Conn.WriteJSON(types.ToPlayerMessage{
			Type:        types.MessageTypeIllegalAction,
			TableState:  obfuscateTableState(r.gameTable.State()),
			PlayerState: getPlayerState(player.Id, r.gameTable),
		})
		return table.State{}, fmt.Errorf("%s by player %s", err.Error(), player.Id)
	}
	r.broadcast(types.ToPlayerMessage{
		Type:         types.MessageTypePlayerAction,
		PlayerAction: types.PlayerAction{Action: action, PlayerId: player.Id},
	})
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
		if tableState.Status == table.Done &&
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

func (r *room) broadcast(msg types.ToPlayerMessage) {
	var err error
	r.playerMap.RLock()
	defer r.playerMap.RUnlock()
	for _, player := range r.playerMap.players {
		if msg.Type == types.MessageTypeTableState {
			msg.PlayerState = getPlayerState(player.Id, r.gameTable)
			if msg.PlayerState.Chips == 0 && r.gameTable.State().Status == table.Done {
				player.Broke = true
			}
		}
		if player.Conn != nil {
			err = retrySend(player, msg)
			if err != nil {
				log.Printf(
					"giving up sending state to player %s in room %s due to too many errors",
					player.Id,
					r.id)
				r.handlePlayerError(player, err)
			}
		}
	}
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

func retrySend(player *types.Player, msg types.ToPlayerMessage) error {
	var (
		backoff time.Duration
		err     error
	)
	backoff = 100 * time.Millisecond
	for i := 0; i < 5; i++ {
		if player.Conn == nil {
			// Player has gone away; probably handled elsewhere
			return nil
		}
		err = player.Conn.WriteJSON(msg)
		if err == nil {
			return nil
		}
		if isClosedConnectionError(err.Error()) {
			player.Conn.Close()
			return err
		}
		log.Printf("error sending state to player %s: %s", player.Id, err.Error())
		time.Sleep(backoff)
		backoff *= 2
	}
	player.Conn.Close()
	return err
}

func (r *room) handlePlayerError(player *types.Player, err error) {
	log.Printf("connection to %s closed with %s", player.Id, err.Error())
	log.Printf("%s is sitting out pending reconnection", player.Id)
	player.Conn = nil
	player.SittingOut = true
	r.broadcast(types.ToPlayerMessage{
		Type:     types.MessageTypePlayerDisconnected,
		PlayerId: player.Id,
	})
	if r.gameTable != nil {
		r.gameTable.SetPlayerDefaulting(player.Id, true)
		if r.gameTable.State().Active.ID == player.Id {
			r.handleMessageFromPlayer(
				types.FromPlayerMessage{
					Type: types.MessageTypePlayerAction,
					Action: table.Action{
						Type: table.Fold,
					},
				},
				player)
		}
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
	/*resultStr := fmt.Sprintf("Table cards: %v\n", tableState.Result.TableCards)
	for _, c := range tableState.Result.Contestants {
		resultStr += fmt.Sprintf("%s: %v\n", c.ID, c.Cards)
	}*/
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

func isClosedConnectionError(errStr string) bool {
	return strings.Contains(errStr, "use of closed network connection") ||
		strings.Contains(errStr, "Broken pipe") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "unexpected EOF") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}
