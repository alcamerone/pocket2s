/*    package "types" provides the type definitions for the Pocket2s server.
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

package types

import (
	"github.com/alcamerone/joker/table"
	"github.com/gorilla/websocket"
)

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
	Conn       *websocket.Conn
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
