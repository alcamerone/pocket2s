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
	MAX_PLAYERS         = 20
	DEFAULT_BUY_IN      = 10000
	DEFAULT_BIG_BLIND   = 100
	DEFAULT_SMALL_BLIND = 50
)

var (
	playerMap     map[string]*types.Player
	playerMapLock *sync.RWMutex
	router        *web.Router
	gameTable     *table.Table
)

func getPlayerIds() []string {
	playerMapLock.RLock()
	defer playerMapLock.RUnlock()
	playerIds := make([]string, len(playerMap))
	for _, player := range playerMap {
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
	playerMap = make(map[string]*types.Player, MAX_PLAYERS)
	playerMapLock = &sync.RWMutex{}
	router = web.New(Context{})
	router.Get("/connect/:playerId", handleConnect)
}

func main() {
	log.Println("starting server on port 2222")
	err := http.ListenAndServe(":2222", router)
	if err != nil {
		log.Fatal("error in main loop: " + err.Error())
	}
}

func handleConnect(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	playerId := req.PathParams["playerId"]

	playerMapLock.RLock()
	tableFull := len(playerMap) > MAX_PLAYERS
	existingPlayer, playerExists := playerMap[playerId]
	playerMapLock.RUnlock()
	if playerExists && existingPlayer.Conn != nil {
		log.Printf("error: a player named %s is already at the table")
		rw.WriteHeader(http.StatusConflict)
		return
	}
	if tableFull {
		log.Printf("error: the table already has the maximum number of players")
		rw.WriteHeader(http.StatusLocked)
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
		playerMapLock.Lock()
		tablePos := len(playerMap)
		playerMap[playerId] = &types.Player{
			Id:       playerId,
			TablePos: tablePos,
			Conn:     conn,
		}
		playerMapLock.Unlock()
		log.Printf("%s has joined", playerId)
	}
	err = conn.WriteJSON(types.ToPlayerMessage{Type: types.MessageTypeHello})
	if err != nil {
		// TODO handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}
	broadcast(types.ToPlayerMessage{
		Type:     types.MessageTypePlayerConnected,
		PlayerId: playerId,
	})
	go listenForPlayerMessages(playerMap[playerId])
}

// @blocking
func listenForPlayerMessages(player *types.Player) {
	var (
		msg types.FromPlayerMessage
		err error
	)
	for {
		err = player.Conn.ReadJSON(&msg)
		if err != nil {
			if isClosedConnectionError(err.Error()) {
				handlePlayerError(player, err)
				player.Conn = nil
				break
			}
			log.Printf("error receiving message from %s: %s", player.Id, err.Error())
			continue
		}
		handleMessageFromPlayer(msg, player)
	}
}

func handleMessageFromPlayer(msg types.FromPlayerMessage, player *types.Player) {
	var (
		state table.State
		err   error
	)
	switch msg.Type {
	case types.MessageTypeReady, types.MessageTypeSitOut:
		isReady := msg.Type == types.MessageTypeReady
		player.Ready = isReady
		player.SittingOut = !isReady
		if gameTable != nil {
			pState := getPlayerState(player.Id, gameTable)
			if pState.ID == player.Id {
				// Player already seated at table
				gameTable.SetPlayerDefaulting(player.Id, !isReady)
			} else {
				gameTable.AddPlayer(player.Id, !isReady)
			}
		}
		if isReady {
			log.Printf("%s is ready", player.Id)
		} else {
			log.Printf("%s is sitting out", player.Id)
		}
		if (gameTable == nil || gameTable.State().Status == table.Done) &&
			playersAreReady() {
			// START THE GAME ALREADY
			if gameTable == nil {
				dealer := hand.NewDealer(
					rand.New(
						randSource.NewConcurrencySafeSource(
							time.Now().UnixNano(),
						),
					),
				)
				gameTable = table.New(
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
					getPlayerIds(),
					getPlayersSittingOut())
				state = gameTable.State()
			} else {
				state = gameTable.NewRound()
			}
		} else {
			return
		}
	case types.MessageTypePlayerAction:
		state, err = handleActionByPlayer(msg.Action, player)
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
	broadcast(types.ToPlayerMessage{
		Type:       types.MessageTypeTableState,
		TableState: tableState,
		Result:     result,
	})
	if result != "" {
		resetPlayersReady()
	}
	return
}

func playersAreReady() bool {
	playerMapLock.RLock()
	defer playerMapLock.RUnlock()
	if len(playerMap) < 2 {
		return false
	}
	var nSittingOut int
	for _, player := range playerMap {
		if !player.Ready && !player.SittingOut {
			return false
		}
		if player.SittingOut {
			nSittingOut++
		}
	}
	if len(playerMap)-nSittingOut < 2 {
		return false
	}
	return true
}

func resetPlayersReady() {
	playerMapLock.RLock()
	defer playerMapLock.RUnlock()
	for id := range playerMap {
		playerMap[id].Ready = false
	}
}

func getPlayersSittingOut() []string {
	sittingOut := make([]string, 0)
	for _, p := range playerMap {
		if p.SittingOut {
			sittingOut = append(sittingOut, p.Id)
		}
	}
	return sittingOut
}

func handleActionByPlayer(action table.Action, player *types.Player) (table.State, error) {
	if player.Id != gameTable.Active().ID {
		return table.State{}, fmt.Errorf(
			"ignoring action request %s from player %s as it is not their turn",
			action.Type.String(),
			player.Id)
	}
	state, err := gameTable.Act(action)
	if err != nil {
		// TODO handle error
		player.Conn.WriteJSON(types.ToPlayerMessage{
			Type:        types.MessageTypeIllegalAction,
			TableState:  obfuscateTableState(gameTable.State()),
			PlayerState: getPlayerState(player.Id, gameTable),
		})
		return table.State{}, fmt.Errorf("%s by player %s", err.Error(), player.Id)
	}
	broadcast(types.ToPlayerMessage{
		Type:         types.MessageTypePlayerAction,
		PlayerAction: types.PlayerAction{Action: action, PlayerId: player.Id},
	})
	return state, err
}

func obfuscateTableState(tableState table.State) table.State {
	tableState.Seats = nil
	active := table.Player{
		ID:         tableState.Active.ID,
		Chips:      tableState.Active.Chips,
		ChipsInPot: tableState.Active.ChipsInPot,
	}
	tableState.Active = active
	return tableState
}

func broadcast(msg types.ToPlayerMessage) {
	var err error
	playerMapLock.RLock()
	defer playerMapLock.RUnlock()
	for _, player := range playerMap {
		if msg.Type == types.MessageTypeTableState {
			msg.PlayerState = getPlayerState(player.Id, gameTable)
		}
		if player.Conn != nil {
			err = retrySend(player, msg)
			if err != nil {
				log.Printf("giving up sending state to player %s due to too many errors", player.Id)
				handlePlayerError(player, err)
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

func handlePlayerError(player *types.Player, err error) {
	log.Printf("connection to %s closed with %s", player.Id, err.Error())
	log.Printf("%s is sitting out pending reconnection", player.Id)
	player.Conn = nil
	player.SittingOut = true
	broadcast(types.ToPlayerMessage{
		Type:     types.MessageTypePlayerDisconnected,
		PlayerId: player.Id,
	})
	if gameTable != nil {
		gameTable.SetPlayerDefaulting(player.Id, true)
		if gameTable.State().Active.ID == player.Id {
			handleMessageFromPlayer(
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
	resultStr := fmt.Sprintf("Table cards: %v\n", tableState.Result.TableCards)
	for _, c := range tableState.Result.Contestants {
		resultStr += fmt.Sprintf("%s: %v\n", c.ID, c.Cards)
	}
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
