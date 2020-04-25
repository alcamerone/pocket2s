/*    package "client/main" defines the test CLI client for the Pocket2s server.
 *    Copyright (C) 2020 Cameron Ekblad.
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
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/types"
	"github.com/gorilla/websocket"
)

const SIT_OUT = "SIT OUT"

var (
	inputReader *bufio.Reader
	conn        *websocket.Conn
	playerId    string
)

const (
	ActionFold  = "FOLD"
	ActionCheck = "CHECK"
	ActionCall  = "CALL"
	ActionBet   = "BET"
	ActionRaise = "RAISE"
	ActionAllIn = "ALLIN"
)

func getInput() (string, error) {
	input, err := inputReader.ReadString('\n')
	input = strings.TrimSpace(input)
	input = strings.ToUpper(input)
	return input, err
}

func main() {
	var err error
	inputReader = bufio.NewReader(os.Stdin)
	fmt.Println("Welcome! Who are you?")
	for {
		// TODO sanitise
		playerId, err = getInput()
		if err == nil {
			break
		}
		log.Printf("error scanning: %s", err.Error()) //TODO remove
		fmt.Println("Sorry, we can't use that name. Try another one:")
	}

	wsDialler := websocket.Dialer{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	conn, _, err = wsDialler.Dial("ws://localhost:2222/connect/"+playerId, nil)
	if err != nil {
		log.Fatalf("error establishing connection with server: %s", err.Error())
	}

	err = mainLoop()
	if err != nil {
		log.Fatalf("error in main gameplay loop: %s", err.Error())
	}
}

func mainLoop() error {
	var (
		msg types.ToPlayerMessage
		err error
	)
	for {
		msg = types.ToPlayerMessage{}
		err = conn.ReadJSON(&msg)
		if err != nil {
			return errors.New("error reading message from server: " + err.Error())
		}
		switch msg.Type {
		case types.MessageTypeHello:
			fmt.Println("Connection established to Pocket2s server!")
			fmt.Println("The game will start when there are two or more players and everyone has marked themselves ready.")
			fmt.Println("Hit Enter when you're ready to start, or type SIT OUT to sit the first round out.")
			awaitPlayerReady(conn, false)
			fmt.Println("Okay! Waiting for other players...")
		case types.MessageTypeTableState, types.MessageTypeIllegalAction:
			if msg.Result != "" {
				fmt.Println(msg.Result)
				if msg.PlayerState.Chips < 1 {
					fmt.Println("You're broke! Press Enter to buy back in, or type SIT OUT to observe the rest of the game.")
				} else {
					fmt.Println("Press Enter when you're ready for the next round, or type SIT OUT to sit out the next round.")
				}
				awaitPlayerReady(conn, msg.PlayerState.Chips < 1)
				fmt.Println("Okay! Waiting for other players...")
				continue
			}
			if msg.Type == types.MessageTypeIllegalAction {
				fmt.Println("Sorry, that action's not allowed.")
			}
			fmt.Printf(
				"Dealer: %s\nSmall Blind: %s\nBig Blind: %s\n",
				msg.TableState.Dealer.ID,
				msg.TableState.SmallBlind.ID,
				msg.TableState.BigBlind.ID)
			fmt.Printf("Cards: %v, Pot: %d\n", msg.TableState.Cards, msg.TableState.Pot)
			if !msg.PlayerState.SittingOut {
				fmt.Printf(
					"Your cards: %v. Your chips: %d, in pot %d\n",
					msg.PlayerState.Cards,
					msg.PlayerState.Chips,
					msg.PlayerState.ChipsInPot)
			}
			if msg.TableState.Active.ID != playerId {
				fmt.Printf("It is %s's turn...\n", msg.TableState.Active.ID)
			} else {
				action := parseTableAction(msg.TableState)
				err := conn.WriteJSON(
					types.FromPlayerMessage{
						Type:   types.MessageTypePlayerAction,
						Action: action,
					},
				)
				if err != nil {
					// TODO handle
					log.Printf("error sending player action to server: %s", err.Error())
				}
			}
		case types.MessageTypePlayerAction:
			fmt.Println(stringifyPlayerAction(msg.PlayerAction))
		case types.MessageTypePlayerConnected:
			fmt.Printf("Player %s has entered the game!\n", msg.PlayerId)
		case types.MessageTypePlayerDisconnected:
			fmt.Printf(
				"Lost connection to player %s, they will sit out until they return.\n",
				msg.PlayerId)
		}
	}
}

func awaitPlayerReady(conn *websocket.Conn, playerIsBroke bool) {
	var (
		input string
		err   error
	)
	for {
		input, err = getInput()
		if err == nil {
			if input == SIT_OUT {
				err = conn.WriteJSON(types.FromPlayerMessage{Type: types.MessageTypeSitOut})
			} else {
				if playerIsBroke {
					err = conn.WriteJSON(types.FromPlayerMessage{Type: types.MessageTypeBuyIn})
				} else {
					err = conn.WriteJSON(types.FromPlayerMessage{Type: types.MessageTypeReady})
				}
			}
			if err != nil {
				log.Printf("error sending user ready message: %s", err.Error()) // TODO remove
				fmt.Println(
					"Sorry, there was a problem letting the server know you're ready. Please press Enter when you're ready to play!")
				continue
			}
			return
		}
		log.Printf("error reading user input: %s", err.Error()) // TODO remove
		fmt.Println(
			"Sorry, something went wrong. Please press Enter when you're ready to play!")
	}
}

func parseTableAction(tableState table.State) table.Action {
	var (
		input string
		err   error
		args  []string
		bet   int
	)
	for {
		fmt.Printf(
			"It is your turn. What would you like to do? (Valid actions are %v)\n",
			validActions(tableState))
		input, err = getInput()
		if err != nil {
			log.Printf("error scanning input: %s", err.Error()) //TODO remove
			fmt.Println("Sorry, I don't know what that means.")
			continue
		}
		args = strings.Split(input, " ")
		switch args[0] {
		case ActionFold:
			return table.Action{Type: table.Fold}
		case ActionCheck:
			return table.Action{Type: table.Check}
		case ActionCall:
			return table.Action{Type: table.Call}
		case ActionBet:
			bet, err = parseBet(args)
			if err != nil {
				fmt.Printf("Sorry, %s.\n", err.Error())
				continue
			}
			return table.Action{Type: table.Bet, Chips: bet}
		case ActionRaise:
			bet, err = parseBet(args)
			if err != nil {
				fmt.Printf("Sorry, %s.\n", err.Error())
				continue
			}
			return table.Action{Type: table.Raise, Chips: bet}
		case ActionAllIn:
			return table.Action{Type: table.AllIn}
		default:
			fmt.Println("Sorry, I don't know what that means.")
		}
	}
}

func validActions(tableState table.State) []string {
	if tableState.Owed == 0 {
		return []string{ActionFold, ActionCheck, ActionBet, ActionAllIn}
	}
	if tableState.Owed > tableState.Active.Chips {
		return []string{ActionFold, ActionCall}
	}
	fmt.Printf("Call cost is %d.\n", tableState.Owed)
	return []string{ActionFold, ActionCall, ActionRaise, ActionAllIn}
}

func parseBet(args []string) (int, error) {
	if len(args) < 2 {
		return 0, errors.New("you need to tell me the amount you'd like to bet")
	}
	amt, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return 0, errors.New("your bet has to be a number")
	}
	if amt < 1 {
		return 0, errors.New("your bet has to be a number greater than 0")
	}
	return int(amt), nil // TODO fix unsafe conversion
}

func stringifyPlayerAction(action types.PlayerAction) string {
	switch action.Type {
	case table.Fold:
		return fmt.Sprintf("%s folds.", action.PlayerId)
	case table.Check:
		return fmt.Sprintf("%s checks.", action.PlayerId)
	case table.Call:
		return fmt.Sprintf("%s calls.", action.PlayerId)
	case table.Bet:
		return fmt.Sprintf("%s bets %d.", action.PlayerId, action.Chips)
	case table.Raise:
		return fmt.Sprintf("%s raises %d.", action.PlayerId, action.Chips)
	case table.AllIn:
		return fmt.Sprintf("%s is all in!", action.PlayerId)
	default:
	}
	log.Printf("unrecognised message type %d", action.Type) //TODO remove
	return fmt.Sprintf("%s: upto bruh?", action.PlayerId)
}
