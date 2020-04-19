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
