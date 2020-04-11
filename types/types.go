package types

import (
	"github.com/alcamerone/joker/table"
	"github.com/gorilla/websocket"
)

type MessageType int

const (
	MessageTypeUnknown MessageType = iota
	MessageTypeHello
	MessageTypeStart
	MessageTypeTableState
	MessageTypePlayerAction
)

var messageTypeStrings = map[MessageType]string{
	MessageTypeUnknown:      "UNKNOWN",
	MessageTypeHello:        "HELLO",
	MessageTypeStart:        "START",
	MessageTypeTableState:   "TABLE_STATE",
	MessageTypePlayerAction: "PLAYER_ACTION",
}

func (t MessageType) String() string {
	return messageTypeStrings[t]
}

type Player struct {
	table.Player
	Conn *websocket.Conn
}

type FromPlayerMessage struct {
	Type   MessageType
	Action table.Action
}

type ToPlayerMessage struct {
	Type         MessageType
	TableState   table.State  `json:",omitempty"`
	PlayerState  table.Player `json:",omitempty"`
	PlayerAction PlayerAction `json:",omitempty"`
	Result       string       `json:",omitempty"`
}

type PlayerAction struct {
	table.Action
	PlayerId string
}
