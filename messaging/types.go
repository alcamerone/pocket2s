/*    package "messaging" provides the type definitions for the Pocket2s server.
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

package messaging

import "github.com/alcamerone/joker/table"

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

type FromPlayerMessage struct {
	RoomId   string
	PlayerId string
	Type     MessageType
	Action   table.Action
}

type ToPlayerMessage struct {
	Type         MessageType
	PlayerId     string `json:",omitempty"`
	Result       string `json:",omitempty"`
	TableState   table.State
	PlayerState  table.Player
	PlayerAction PlayerAction
}

type PlayerAction struct {
	table.Action
	PlayerId string
}
