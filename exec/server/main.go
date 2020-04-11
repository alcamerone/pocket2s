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
	MAX_PLAYERS         = 8
	DEFAULT_BUY_IN      = 10000
	DEFAULT_BIG_BLIND   = 100
	DEFAULT_SMALL_BLIND = 50
)

var (
	playerMap     map[string]types.Player
	playerMapLock *sync.RWMutex
	router        *web.Router
	gameTable     *table.Table
)

func getPlayerIds() []string {
	players := gameTable.Seats()
	playerIds := make([]string, len(players))
	for i, player := range players {
		playerIds[i] = player.ID
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
	playerMap = make(map[string]types.Player, MAX_PLAYERS)
	playerMapLock = &sync.RWMutex{}
	router = web.New(Context{})
	router.Get("/connect/:playerId", handleConnect)
}

func main() {
	http.ListenAndServe(":80", router)
}

func handleConnect(ctx *Context, rw web.ResponseWriter, req *web.Request) {
	playerId := req.PathParams["playerId"]

	playerMapLock.RLock()
	tableFull := len(playerMap) > MAX_PLAYERS
	_, ok := playerMap[playerId]
	playerMapLock.RUnlock()
	if ok {
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

	playerMapLock.Lock()
	defer playerMapLock.Unlock()
	playerMap[playerId] = types.Player{Player: table.Player{ID: playerId}, Conn: conn}
	log.Printf("player %s has joined", playerId)
	err = conn.WriteJSON(types.ToPlayerMessage{Type: types.MessageTypeHello})
	if err != nil {
		// TODO: handle
		log.Printf("error sending \"hello\" message to player: %s", err.Error())
	}
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
				// TODO handle
				log.Printf("connection to %s closed with %s", player.ID, err.Error())
				break
			}
			log.Printf("error receiving message from %s: %s", player.ID, err.Error())
			continue
		}
		handleMessageFromPlayer(msg, player)
	}
}

func handleMessageFromPlayer(msg types.FromPlayerMessage, player *types.Player) error {
	switch msg.Type {
	case types.MessageTypeStart:
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
					Limit: table.NoLimit,
				},
				getPlayerIds())
		}
	case types.MessageTypePlayerAction:
		handleActionByPlayer(msg.Action, player)
	default:
		return fmt.Errorf("invalid message type %d", msg.Type)
	}
	tableState := obfuscateTableState(gameTable.State())
	broadcast(types.ToPlayerMessage{
		Type:       types.MessageTypeTableState,
		TableState: tableState,
		Result:     getResult(gameTable.State()),
	})
	return nil
}

func handleActionByPlayer(action table.Action, player *types.Player) {
	if player.ID != gameTable.Active().ID {
		log.Printf("ignoring action request %s from player %s as it is not their turn")
	}
	err := gameTable.Act(action)
	if err != nil {
		log.Printf("%s by player %s", err.Error(), player.ID)
	}
	broadcast(types.ToPlayerMessage{
		Type:         types.MessageTypePlayerAction,
		PlayerAction: types.PlayerAction{Action: action, PlayerId: player.ID},
	})
}

func obfuscateTableState(tableState table.State) table.State {
	tableState.Seats = nil
	active := table.Player{
		ID: tableState.Active.ID,
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
			msg.PlayerState = getPlayerState(player, gameTable)
		}
		err = retrySend(player, msg)
		if err != nil {
			log.Printf("giving up sending state to player %s due to too many errors", player.ID)
			// TODO: Make this player fold next turn
			// and sit out until their connection recovers
		}
	}
}

func getPlayerState(player types.Player, t *table.Table) table.Player {
	for _, s := range t.Seats() {
		if s.ID == player.ID {
			return s
		}
	}
	// TODO handle
	log.Printf("could not find player %s at table", player.ID)
	return table.Player{}
}

func retrySend(player types.Player, msg types.ToPlayerMessage) error {
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
		log.Printf("error sending state to player %s: %s", player.ID, err.Error())
		time.Sleep(backoff)
		backoff *= 2
	}
	return err
}

func getResult(tableState table.State) string {
	if tableState.Round != table.PreFlop || tableState.Winners == nil {
		return ""
	}
	winningHands := make([]string, len(tableState.Winners))
	var h *hand.Hand
	for i, winner := range tableState.Winners {
		h = hand.New(append(winner.Cards, tableState.Cards...))
		winningHands[i] = h.String()
	}
	if len(winningHands) == 1 {
		return fmt.Sprintf("%s wins with %s", tableState.Winners[0].ID, winningHands[0])
	}
	var resultStr string
	for _, winner := range tableState.Winners {
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
		strings.Contains(errStr, "unexpected EOF") ||
		strings.Contains(errStr, "going away") ||
		strings.Contains(errStr, "connection reset by peer")
}
