package db

import (
	"context"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/randSource"
	"github.com/alcamerone/pocket2s/room"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type RoomStore interface {
	GetRoom(ctx context.Context, id string) (*room.Room, error)
	GetAllRooms(ctx context.Context) ([]*room.Room, error)
	NewRoom(ctx context.Context, room *room.Room) error
	UpdateRoom(ctx context.Context, room *room.Room) error
	DeleteRoom(ctx context.Context, id string) error
}

type InMemoryRoomStore struct {
	rooms *cmap.ConcurrentMap[string, *room.Room]
}

func NewInMemoryRoomStore() *InMemoryRoomStore {
	return &InMemoryRoomStore{
		rooms: cmap.New[string, *room.Room](),
	}
}

func (s *InMemoryRoomStore) GetRoom(_ context.Context, id string) (*room.Room, error) {
	room, _ := s.rooms.Get(id)
	return room, nil
}

func (s *InMemoryRoomStore) GetAllRooms(_ context.Context) ([]*room.Room, error) {
	return slices.Collect(s.rooms.Values()), nil
}

func (s *InMemoryRoomStore) NewRoom(_ context.Context, room *room.Room) error {
	s.rooms.Set(room.Id, room)
	return nil
}

func (s *InMemoryRoomStore) UpdateRoom(_ context.Context, room *room.Room) error {
	// Dummy implementation for now
	// TODO Make rooms immutable
	return nil
}

func (s *InMemoryRoomStore) DeleteRoom(_ context.Context, id string) error {
	s.rooms.Delete(id)
	return nil
}

type DDBRoomStore struct {
	ddbClient *dynamodb.Client
	tableName string
}

func NewDDBRoomStore(ddb *dynamodb.Client, tableName string) *DDBRoomStore {
	return &DDBRoomStore{
		ddbClient: ddb,
		tableName: tableName,
	}
}

func (s *DDBRoomStore) GetRoom(ctx context.Context, id string) (*room.Room, error) {
	roomId, err := attributevalue.Marshal(id)
	if err != nil {
		return nil, err
	}
	resp, err := s.ddbClient.GetItem(ctx, &dynamodb.GetItemInput{
		Key:       map[string]ddbTypes.AttributeValue{"Id": roomId},
		TableName: &s.tableName})
	if err != nil {
		return nil, err
	}

	var state room.State
	err = attributevalue.UnmarshalMap(resp.Item, &s)
	if err != nil {
		return nil, err
	}

	return &room.Room{
		Id:        state.Id,
		Opts:      state.Opts,
		PlayerMap: state.PlayerMap,
		GameTable: table.NewFromState(
			hand.NewDealer(
				rand.New(
					randSource.NewConcurrencySafeSource(
						uint64(time.Now().UnixNano()),
						uint64(time.Now().UnixNano())+uint64(time.Millisecond),
					),
				),
			),
			state),
	}, nil
}

func (s *DDBRoomStore) GetAllRooms(ctx context.Context) ([]*room.Room, error) {
	// Dummy implementation; only used by in-memory store
	return nil, nil
}

func (s *DDBRoomStore) NewRoom(ctx context.Context, r *room.Room) error {
	// Both use dynamodbClient.PutItem underneath
	return s.UpdateRoom(ctx, r)
}

func (s *DDBRoomStore) UpdateRoom(ctx context.Context, r *room.Room) error {
	ddbItem, err := attributevalue.MarshalMap(r)
	if err != nil {
		return err
	}
	_, err = s.ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.tableName,
		Item:      ddbItem,
	})
	return err
}

func (s *DDBRoomStore) DeleteRoom(ctx context.Context, id string) error {
	roomId, err := attributevalue.Marshal(id)
	if err != nil {
		return err
	}
	_, err = s.ddbClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &s.tableName,
		Key:       map[string]ddbTypes.AttributeValue{"Id": roomId},
	})
	return err
}
