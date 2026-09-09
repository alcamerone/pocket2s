package db

import (
	"context"
	"slices"

	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/types"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type RoomStore interface {
	GetRoom(ctx context.Context, id string) (*types.Room, error)
	GetAllRooms(ctx context.Context) ([]*types.Room, error)
	NewRoom(ctx context.Context, room *types.Room) error
	UpdateRoom(ctx context.Context, room *types.Room) error
	DeleteRoom(ctx context.Context, id string) error
}

type InMemoryRoomStore struct {
	rooms *cmap.ConcurrentMap[string, *types.Room]
}

func NewInMemoryRoomStore() *InMemoryRoomStore {
	return &InMemoryRoomStore{
		rooms: cmap.New[string, *types.Room](),
	}
}

func (s *InMemoryRoomStore) GetRoom(_ context.Context, id string) (*types.Room, error) {
	room, _ := s.rooms.Get(id)
	return room, nil
}

func (s *InMemoryRoomStore) GetAllRooms(_ context.Context) ([]*types.Room, error) {
	return slices.Collect(s.rooms.Values()), nil
}

func (s *InMemoryRoomStore) NewRoom(_ context.Context, room *types.Room) error {
	s.rooms.Set(room.Id, room)
	return nil
}

func (s *InMemoryRoomStore) UpdateRoom(_ context.Context, room *types.Room) error {
	// Dummy implementation for now
	// TODO Make rooms idempotent
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

func (s *DDBRoomStore) GetRoom(ctx context.Context, id string) (*types.Room, error) {
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

	var room types.Room
	err = attributevalue.UnmarshalMap(resp.Item, &room)
	if err != nil {
		return nil, err
	}
	return &room, nil
}

func (s *DDBRoomStore) GetAllRooms(ctx context.Context) ([]*types.Room, error) {
	// Dummy implementation; only used by in-memory store
	return nil, nil
}

func (s *DDBRoomStore) NewRoom(ctx context.Context, r *types.Room) error {
	// Both use dynamodbClient.PutItem underneath
	return s.UpdateRoom(ctx, r)
}

func (s *DDBRoomStore) UpdateRoom(ctx context.Context, r *types.Room) error {
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
