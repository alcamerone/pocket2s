package db

import (
	"context"
	"slices"

	"github.com/alcamerone/pocket2s/server/cmap"
	"github.com/alcamerone/pocket2s/server/room"
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
	rooms *cmap.ConcurrentMap[string, room.State]
}

func NewInMemoryRoomStore() *InMemoryRoomStore {
	return &InMemoryRoomStore{
		rooms: cmap.New[string, room.State](),
	}
}

func (s *InMemoryRoomStore) GetRoom(_ context.Context, id string) (*room.Room, error) {
	state, _ := s.rooms.Get(id)
	return room.RehydrateRoom(state), nil
}

func (s *InMemoryRoomStore) GetAllRooms(_ context.Context) ([]*room.Room, error) {
	states := slices.Collect(s.rooms.Values())
	rooms := make([]*room.Room, len(states))
	for i := range states {
		rooms[i] = room.RehydrateRoom(states[i])
	}
	return rooms, nil
}

func (s *InMemoryRoomStore) NewRoom(ctx context.Context, r *room.Room) error {
	return s.UpdateRoom(ctx, r)
}

func (s *InMemoryRoomStore) UpdateRoom(_ context.Context, r *room.Room) error {
	s.rooms.Set(r.Id, r.State())
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
	return room.RehydrateRoom(state), nil
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
	ddbItem, err := attributevalue.MarshalMap(r.State())
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
