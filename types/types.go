/*    package "types" provides the type definitions for the Pocket2s server.
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

package types

import (
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/cmap"
)

const MAX_PLAYERS = 6

type MessageType int

const (
	MessageTypeUnknown MessageType = iota
	MessageTypeHello
	MessageTypeReady
	MessageTypeSitOut
	MessageTypeBuyIn
	MessageTypeTableState
	MessageTypePlayerAction
	MessageTypeIllegalAction
	MessageTypePlayerConnected
	MessageTypePlayerDisconnected
)

type Player struct {
	Id         string
	TablePos   int
	Ready      bool
	SittingOut bool
	Broke      bool
}

type FromPlayerMessage struct {
	Type   MessageType
	Action table.Action
}

type ToPlayerMessage struct {
	Type         MessageType
	PlayerId     string       `json:",omitempty"`
	TableState   table.State  `json:",omitempty"`
	PlayerState  table.Player `json:",omitempty"`
	PlayerAction PlayerAction `json:",omitempty"`
	Result       string       `json:",omitempty"`
}

type PlayerAction struct {
	table.Action
	PlayerId string
}

type Room struct {
	Id                   string
	Opts                 RoomOpts
	PlayerMap            *cmap.ConcurrentMap[string, *Player]
	GameTable            *table.Table
	cancelSelfDestructCh chan struct{}
}

func (r *Room) GetPlayerIds() []string {
	playerIds := make([]string, r.PlayerMap.Len())
	for player := range r.PlayerMap.Values() {
		playerIds[player.TablePos] = player.Id
	}
	return playerIds
}

func (r *Room) PlayersAreReady() bool {
	if r.PlayerMap.Len() < 2 {
		return false
	}
	var nSittingOut int
	for _, player := range r.PlayerMap.All() {
		if !player.Ready && !player.SittingOut && !player.Broke {
			return false
		}
		if player.SittingOut || player.Broke {
			nSittingOut++
		}
	}
	if r.PlayerMap.Len()-nSittingOut < 2 {
		return false
	}
	return true
}

func (r *Room) ResetPlayersReady() {
	for p := range r.PlayerMap.Values() {
		p.Ready = false
	}
}

func (r *Room) GetPlayersSittingOut() []string {
	sittingOut := make([]string, 0)
	for p := range r.PlayerMap.Values() {
		if p.SittingOut {
			sittingOut = append(sittingOut, p.Id)
		}
	}
	return sittingOut
}

type RoomOpts struct {
	BuyIn      int
	BigBlind   int
	SmallBlind int
	Ante       int
}
