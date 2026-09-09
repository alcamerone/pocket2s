package db

import (
	"errors"

	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/conn"
)

var ErrNotFound = errors.New("connection not found")

type ConnectionStore interface {
	NewConnection(string, conn.WebSocket) error
	GetConnection(string) (conn.WebSocket, error)
	DeleteConnection(string) error
}

type InMemoryConnectionStore struct {
	conns *cmap.ConcurrentMap[string, conn.WebSocket]
}

func NewInMemoryConnectionStore() *InMemoryConnectionStore {
	return &InMemoryConnectionStore{
		conns: cmap.New[string, conn.WebSocket](),
	}
}

func (s *InMemoryConnectionStore) NewConnection(id string, c conn.WebSocket) error {
	s.conns.Set(id, c)
	return nil
}

func (s *InMemoryConnectionStore) GetConnection(id string) (conn.WebSocket, error) {
	c, _ := s.conns.Get(id)
	if c == nil {
		return nil, ErrNotFound
	}
	return c, nil
}

func (s *InMemoryConnectionStore) DeleteConnection(id string) error {
	// NB: The connection store is not responsible for closing connections,
	// only storing and retrieving them. This should be handled elsewhere.
	s.conns.Delete(id)
	return nil
}
