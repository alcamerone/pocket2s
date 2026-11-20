package db

import (
	"context"
	"errors"
	"testing"

	"github.com/alcamerone/pocket2s/server/room"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var testOpts = room.RoomOpts{BuyIn: 100, BigBlind: 10, SmallBlind: 5}

// ---- in-memory store ----

func TestInMemoryRoomStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewInMemoryRoomStore()

	if _, err := s.GetRoom(ctx, "missing"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("GetRoom on empty store: %v", err)
	}

	r := room.NewRoom("r1", testOpts)
	if err := s.NewRoom(ctx, r); err != nil {
		t.Fatalf("NewRoom: %v", err)
	}
	got, err := s.GetRoom(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRoom: %v", err)
	}
	if got.Id != "r1" || got.Opts != testOpts {
		t.Fatalf("round-tripped room wrong: %+v", got)
	}

	// Update with a changed option and confirm it is persisted
	r.Opts.BigBlind = 50
	if err := s.UpdateRoom(ctx, r); err != nil {
		t.Fatalf("UpdateRoom: %v", err)
	}
	got, _ = s.GetRoom(ctx, "r1")
	if got.Opts.BigBlind != 50 {
		t.Fatalf("update not persisted: %+v", got.Opts)
	}

	all, err := s.GetAllRooms(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("GetAllRooms = %d, %v", len(all), err)
	}

	if err := s.DeleteRoom(ctx, "r1"); err != nil {
		t.Fatalf("DeleteRoom: %v", err)
	}
	if _, err := s.GetRoom(ctx, "r1"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("room should be deleted, got %v", err)
	}
}

// ---- DynamoDB store ----

// mockDDB is a function-field fake of ddbAPI that records the last input of each call.
type mockDDB struct {
	getItem    func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	putItem    func(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error)
	deleteItem func(*dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error)

	gotGet    *dynamodb.GetItemInput
	gotPut    *dynamodb.PutItemInput
	gotDelete *dynamodb.DeleteItemInput
}

func (m *mockDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	m.gotGet = in
	return m.getItem(in)
}

func (m *mockDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.gotPut = in
	return m.putItem(in)
}

func (m *mockDDB) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	m.gotDelete = in
	return m.deleteItem(in)
}

func putOK(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func putErr(err error) func(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return func(*dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) { return nil, err }
}

func getItem(item map[string]ddbAV) func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
	return func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
		return &dynamodb.GetItemOutput{Item: item}, nil
	}
}

func newDDBStore(m *mockDDB) *DDBRoomStore {
	return NewDDBRoomStore(m, "rooms")
}

func TestDDBGetRoom(t *testing.T) {
	ctx := context.Background()

	t.Run("not found when no item", func(t *testing.T) {
		s := newDDBStore(&mockDDB{getItem: getItem(nil)})
		if _, err := s.GetRoom(ctx, "x"); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("found and rehydrated", func(t *testing.T) {
		m := &mockDDB{getItem: getItem(map[string]ddbAV{
			"Id": &ddbTypes.AttributeValueMemberS{Value: "x"},
			"Opts": &ddbTypes.AttributeValueMemberM{Value: map[string]ddbAV{
				"BuyIn":      &ddbTypes.AttributeValueMemberN{Value: "100"},
				"BigBlind":   &ddbTypes.AttributeValueMemberN{Value: "10"},
				"SmallBlind": &ddbTypes.AttributeValueMemberN{Value: "5"},
			}},
		})}
		r, err := newDDBStore(m).GetRoom(ctx, "x")
		if err != nil {
			t.Fatalf("GetRoom: %v", err)
		}
		if r.Id != "x" || r.Opts.BuyIn != 100 || r.GameTable != nil {
			t.Fatalf("unexpected room: %+v", r)
		}
		key, ok := m.gotGet.Key["Id"].(*ddbTypes.AttributeValueMemberS)
		if !ok || key.Value != "x" || *m.gotGet.TableName != "rooms" {
			t.Fatalf("wrong request: %+v", m.gotGet)
		}
	})

	t.Run("corrupt item fails to unmarshal", func(t *testing.T) {
		s := newDDBStore(&mockDDB{getItem: getItem(map[string]ddbAV{
			"Id":   &ddbTypes.AttributeValueMemberS{Value: "x"},
			"Opts": &ddbTypes.AttributeValueMemberS{Value: "not-a-struct"},
		})})
		if _, err := s.GetRoom(ctx, "x"); err == nil {
			t.Fatal("expected unmarshal error")
		}
	})

	t.Run("client error", func(t *testing.T) {
		boom := errors.New("boom")
		s := newDDBStore(&mockDDB{getItem: func(*dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
			return nil, boom
		}})
		if _, err := s.GetRoom(ctx, "x"); !errors.Is(err, boom) {
			t.Fatalf("got %v want %v", err, boom)
		}
	})
}

func TestDDBGetAllRoomsIsEmpty(t *testing.T) {
	rooms, err := newDDBStore(&mockDDB{}).GetAllRooms(context.Background())
	if err != nil || rooms != nil {
		t.Fatalf("GetAllRooms = %v, %v", rooms, err)
	}
}

func TestDDBNewRoom(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		m := &mockDDB{putItem: putOK}
		if err := newDDBStore(m).NewRoom(ctx, room.NewRoom("n", testOpts)); err != nil {
			t.Fatalf("NewRoom: %v", err)
		}
		if got := *m.gotPut.ConditionExpression; got != "attribute_not_exists(#id)" {
			t.Fatalf("missing not-exists condition: %v", got)
		}
		if id, ok := m.gotPut.Item["Id"].(*ddbTypes.AttributeValueMemberS); !ok || id.Value != "n" {
			t.Fatalf("item Id wrong: %+v", m.gotPut.Item["Id"])
		}
	})

	t.Run("already exists", func(t *testing.T) {
		s := newDDBStore(&mockDDB{putItem: putErr(&ddbTypes.ConditionalCheckFailedException{})})
		if err := s.NewRoom(ctx, room.NewRoom("n", testOpts)); !errors.Is(err, ErrRoomExists) {
			t.Fatalf("got %v want ErrRoomExists", err)
		}
	})

	t.Run("other failure", func(t *testing.T) {
		s := newDDBStore(&mockDDB{putItem: putErr(errors.New("boom"))})
		err := s.NewRoom(ctx, room.NewRoom("n", testOpts))
		if err == nil || errors.Is(err, ErrRoomExists) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("marshal failure", func(t *testing.T) {
		defer swapMarshal(func(any) (map[string]ddbAV, error) {
			return nil, errors.New("marshal boom")
		})()
		if err := newDDBStore(&mockDDB{}).NewRoom(ctx, room.NewRoom("n", testOpts)); err == nil {
			t.Fatal("expected marshal error")
		}
	})
}

func TestDDBUpdateRoom(t *testing.T) {
	ctx := context.Background()

	t.Run("success increments versions", func(t *testing.T) {
		m := &mockDDB{putItem: putOK}
		r := room.NewRoom("u", testOpts)
		if err := newDDBStore(m).UpdateRoom(ctx, r); err != nil {
			t.Fatalf("UpdateRoom: %v", err)
		}
		if r.Version != 1 {
			t.Fatalf("source room version = %d want 1", r.Version)
		}
		if got := *m.gotPut.ConditionExpression; got != "#v = :curr" {
			t.Fatalf("missing version condition: %v", got)
		}
		if cur, ok := m.gotPut.ExpressionAttributeValues[":curr"].(*ddbTypes.AttributeValueMemberN); !ok || cur.Value != "0" {
			t.Fatalf("condition should compare against old version 0: %+v", m.gotPut.ExpressionAttributeValues)
		}
	})

	t.Run("stale write is a version conflict", func(t *testing.T) {
		s := newDDBStore(&mockDDB{putItem: putErr(&ddbTypes.ConditionalCheckFailedException{})})
		r := room.NewRoom("u", testOpts)
		if err := s.UpdateRoom(ctx, r); !errors.Is(err, ErrVersionConflict) {
			t.Fatalf("got %v want ErrVersionConflict", err)
		}
		if r.Version != 0 {
			t.Fatalf("version must not change on conflict, got %d", r.Version)
		}
	})

	t.Run("other failure", func(t *testing.T) {
		s := newDDBStore(&mockDDB{putItem: putErr(errors.New("boom"))})
		err := s.UpdateRoom(ctx, room.NewRoom("u", testOpts))
		if err == nil || errors.Is(err, ErrVersionConflict) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("marshal failure", func(t *testing.T) {
		defer swapMarshal(func(any) (map[string]ddbAV, error) {
			return nil, errors.New("marshal boom")
		})()
		if err := newDDBStore(&mockDDB{}).UpdateRoom(ctx, room.NewRoom("u", testOpts)); err == nil {
			t.Fatal("expected marshal error")
		}
	})
}

func TestDDBDeleteRoom(t *testing.T) {
	ctx := context.Background()

	m := &mockDDB{deleteItem: func(*dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error) {
		return &dynamodb.DeleteItemOutput{}, nil
	}}
	if err := newDDBStore(m).DeleteRoom(ctx, "gone"); err != nil {
		t.Fatalf("DeleteRoom: %v", err)
	}
	if key, ok := m.gotDelete.Key["Id"].(*ddbTypes.AttributeValueMemberS); !ok || key.Value != "gone" {
		t.Fatalf("wrong key: %+v", m.gotDelete.Key)
	}

	boom := errors.New("boom")
	s := newDDBStore(&mockDDB{deleteItem: func(*dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error) {
		return nil, boom
	}})
	if err := s.DeleteRoom(ctx, "gone"); !errors.Is(err, boom) {
		t.Fatalf("got %v want %v", err, boom)
	}
}

// Compile-time checks that the stores satisfy the interface and the real client satisfies ddbAPI.
var _ RoomStore = (*DDBRoomStore)(nil)
var _ RoomStore = (*InMemoryRoomStore)(nil)
var _ ddbAPI = (*dynamodb.Client)(nil)
