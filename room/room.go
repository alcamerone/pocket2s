package room

import (
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/messaging"
	"github.com/alcamerone/pocket2s/randSource"
)

type Player struct {
	Id         string
	TablePos   int
	Ready      bool
	SittingOut bool
	Broke      bool
}

type RoomOpts struct {
	BuyIn      int
	BigBlind   int
	SmallBlind int
	Ante       int
}

type Room struct {
	Id        string
	Opts      RoomOpts
	PlayerMap map[string]Player
	GameTable *table.Table
}

type State struct {
	Id             string
	Opts           RoomOpts
	PlayerMap      map[string]Player
	GameTableState table.State
}

func (r *Room) State() State {
	return State{
		Id:             r.Id,
		Opts:           r.Opts,
		PlayerMap:      r.PlayerMap,
		GameTableState: r.GameTable.State(),
	}
}

func (r *Room) GetPlayerIds() []string {
	playerIds := make([]string, len(r.PlayerMap))
	for _, player := range r.PlayerMap {
		playerIds[player.TablePos] = player.Id
	}
	return playerIds
}

func (r *Room) PlayersAreReady() bool {
	if len(r.PlayerMap) < 2 {
		return false
	}
	var nSittingOut int
	for _, player := range r.PlayerMap {
		if !player.Ready && !player.SittingOut && !player.Broke {
			return false
		}
		if player.SittingOut || player.Broke {
			nSittingOut++
		}
	}
	if len(r.PlayerMap)-nSittingOut < 2 {
		return false
	}
	return true
}

func (r *Room) ResetPlayersReady() {
	for _, p := range r.PlayerMap {
		p.Ready = false
	}
}

func (r *Room) GetPlayersSittingOut() []string {
	sittingOut := make([]string, 0)
	for _, p := range r.PlayerMap {
		if p.SittingOut {
			sittingOut = append(sittingOut, p.Id)
		}
	}
	return sittingOut
}

func (r *Room) GetPlayerState(playerId string) table.Player {
	for _, s := range r.GameTable.Seats() {
		if s.ID == playerId {
			return s
		}
	}
	log.Printf("Could not find player %s at table", playerId)
	return table.Player{}
}

func (r *Room) GetObfuscatedTableState() table.State {
	tableState := r.GameTable.State()
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
			r.PlayerIsContesting(player.ID) {
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

func (r *Room) PlayerIsContesting(playerId string) bool {
	for _, contestant := range r.GameTable.State().Result.Contestants {
		if contestant.ID == playerId {
			return true
		}
	}
	return false
}

func (r *Room) GetResultStr() string {
	tableState := r.GameTable.State()
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

func (r *Room) HandleMessageFromPlayer(msg messaging.FromPlayerMessage) (State, error) {
	var (
		state State
		err   error
	)
	player, _ := r.PlayerMap[msg.PlayerId] // TODO handle player not found? Should this happen up-stream?
	switch msg.Type {
	case messaging.MessageTypeReady, messaging.MessageTypeSitOut:
		isReady := msg.Type == messaging.MessageTypeReady
		player.Ready = isReady
		player.SittingOut = !isReady
		if r.GameTable != nil {
			pState := r.GetPlayerState(player.Id)
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
				r.GameTable = table.New(
					hand.NewDealer(
						rand.New(
							randSource.NewConcurrencySafeSource(
								uint64(time.Now().UnixNano()),
								uint64(time.Now().UnixNano())+uint64(time.Millisecond),
							),
						),
					),
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
			} else {
				r.GameTable.NewRound()
			}
			state = r.State()
		} else {
			return state, nil
		}
	case messaging.MessageTypeBuyIn:
		if r.GameTable != nil {
			err = r.GameTable.BuyPlayerIn(player.Id)
			if err != nil {
				log.Printf("error buying %s in; not found", player.Id)
			}
			player.Broke = false
			return r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
				PlayerId: msg.PlayerId,
				Type:     messaging.MessageTypeReady})
		}
	case messaging.MessageTypePlayerAction:
		if player.Id != r.GameTable.Active().ID {
			return State{}, fmt.Errorf(
				"ignoring action request %s from player %s as it is not their turn",
				msg.Action.Type.String(),
				player.Id)
		}
		_, err := r.GameTable.Act(msg.Action)
		if err != nil {
			// cId := fmt.Sprintf("%s-%s", r.Id, p.Id)
			// pc, cErr := cs.GetConnection(cId)
			// if cErr != nil {
			// 	handlePlayerError(p, cErr, r, cs)
			// }
			// pc.WriteJSON(messaging.ToPlayerMessage{ // TODO Implement
			// 	Type:        messaging.MessageTypeIllegalAction,
			// 	TableState:  r.GetObfuscatedTableState(),
			// 	PlayerState: r.GetPlayerState(p.Id),
			// })
			return State{}, fmt.Errorf("%s by player %s", err.Error(), player.Id)
		}
		broadcast(
			r,
			messaging.ToPlayerMessage{
				Type:         messaging.MessageTypePlayerAction,
				PlayerAction: messaging.PlayerAction{Action: msg.Action, PlayerId: player.Id},
			})
		return r.State(), err
	default:
		return State{}, fmt.Errorf("invalid message type %d", msg.Type)
	}
	tableState := r.GetObfuscatedTableState()
	result := r.GetResultStr()
	if result != "" {
		r.ResetPlayersReady()
	}
	broadcast(
		r,
		messaging.ToPlayerMessage{
			Type:       messaging.MessageTypeTableState,
			TableState: tableState,
			Result:     result,
		})
	return r.State(), nil
}
