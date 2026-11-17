package db

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/alcamerone/pocket2s/server/cmap"
	"github.com/alcamerone/pocket2s/server/room"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var (
	ErrRoomNotFound    = errors.New("db: no room found for provided ID")
	ErrRoomExists      = errors.New("db: room already exists")
	ErrVersionConflict = errors.New("db: attempted to write a stale room state")
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
	state, ok := s.rooms.Get(id)
	if !ok {
		return nil, ErrRoomNotFound
	}
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
	if resp.Item == nil {
		return nil, ErrRoomNotFound
	}

	var state room.State
	err = attributevalue.UnmarshalMap(resp.Item, &state)
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
	rs := r.State()
	rs.ExpiresAt = time.Now().Add(3 * time.Hour).Unix()
	ddbItem, err := attributevalue.MarshalMap(rs)
	if err != nil {
		return err
	}
	_, err = s.ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                &s.tableName,
		Item:                     ddbItem,
		ConditionExpression:      aws.String("attribute_not_exists(#id)"),
		ExpressionAttributeNames: map[string]string{"#id": "Id"},
	})
	if err != nil {
		if _, isConditionalCheckFailedException := errors.AsType[*ddbTypes.ConditionalCheckFailedException](err); isConditionalCheckFailedException {
			return ErrRoomExists
		}
		return err
	}
	return nil
}

func (s *DDBRoomStore) UpdateRoom(ctx context.Context, r *room.Room) error {
	rState := r.State()
	curr := rState.Version
	rState.Version += 1
	rState.ExpiresAt = time.Now().Add(3 * time.Hour).Unix()
	ddbItem, err := attributevalue.MarshalMap(rState)
	if err != nil {
		return err
	}
	_, err = s.ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                &s.tableName,
		Item:                     ddbItem,
		ConditionExpression:      aws.String("#v = :curr"),
		ExpressionAttributeNames: map[string]string{"#v": "Version"},
		ExpressionAttributeValues: map[string]ddbTypes.AttributeValue{
			":curr": &ddbTypes.AttributeValueMemberN{Value: strconv.FormatInt(curr, 10)},
		},
	})
	if err != nil {
		if _, isConditionalCheckFailedException := errors.AsType[*ddbTypes.ConditionalCheckFailedException](err); isConditionalCheckFailedException {
			return ErrVersionConflict
		}
		return err
	}
	// Update on source room for future writes
	r.Version += 1
	return nil
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
