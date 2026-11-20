package room

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alcamerone/joker/hand"
	"github.com/alcamerone/joker/table"
	"github.com/alcamerone/pocket2s/server/messaging"
)

var testOpts = RoomOpts{BuyIn: 100, BigBlind: 10, SmallBlind: 5, Ante: 0}

// newSeatedRoom returns a room with the given players, all already marked ready
// and with a live table started via the normal message flow.
func newSeatedRoom(t *testing.T, ids ...string) *Room {
	t.Helper()
	r := NewRoom("room", testOpts)
	for i, id := range ids {
		r.PlayerMap[id] = &Player{Id: id, TablePos: i}
	}
	for _, id := range ids {
		if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
			PlayerId: id, Type: messaging.MessageTypeReady,
		}); err != nil {
			t.Fatalf("ready %s: %v", id, err)
		}
	}
	if r.GameTable == nil {
		t.Fatal("expected table to be created once all players are ready")
	}
	return r
}

func activeID(r *Room) string {
	return r.GameTable.Active().ID
}

func TestNewRoom(t *testing.T) {
	r := NewRoom("abc", testOpts)
	if r.Id != "abc" || r.Opts != testOpts || r.GameTable != nil || r.PlayerMap == nil {
		t.Fatalf("unexpected room: %+v", r)
	}
}

func TestRehydrateRoomWithoutTable(t *testing.T) {
	s := State{Id: "x", Opts: testOpts, PlayerMap: map[string]Player{"a": {Id: "a", TablePos: 0}}}
	r := RehydrateRoom(s)
	if r.GameTable != nil {
		t.Fatal("expected no table for zero table state")
	}
	if r.Id != "x" || r.PlayerMap["a"] == nil || r.PlayerMap["a"].Id != "a" {
		t.Fatalf("unexpected rehydrated room: %+v", r)
	}
}

// advanceToFlop plays the pre-flop betting so the table has community cards,
// chips in the pot, a remaining deck and non-trivial active/blind indices.
func advanceToFlop(t *testing.T, r *Room) {
	t.Helper()
	for i := 0; i < 10 && len(r.GameTable.State().Cards) == 0; i++ {
		var err error
		if slices.Contains(r.GameTable.LegalActions(), table.Check) {
			_, err = r.GameTable.Check()
		} else {
			_, err = r.GameTable.Call()
		}
		if err != nil {
			t.Fatalf("advancing pre-flop: %v", err)
		}
	}
	if st := r.GameTable.State(); len(st.Cards) == 0 {
		t.Fatalf("expected community cards after pre-flop betting, got %+v", st)
	}
}

func TestRehydrateRoomWithTable(t *testing.T) {
	src := newSeatedRoom(t, "a", "b", "c")
	advanceToFlop(t, src)
	src.Version = 7
	s := src.State()

	// Guard against the fixture silently becoming trivial: the non-primitive
	// parts of the table state must actually be populated.
	gs := s.GameTableState
	if len(gs.Seats) != 3 || len(gs.Cards) == 0 || len(gs.Deck) == 0 || gs.Pot == 0 {
		t.Fatalf("fixture too trivial: %+v", gs)
	}
	for _, seat := range gs.Seats {
		if len(seat.Cards) != 2 {
			t.Fatalf("seat %s should hold 2 hole cards, has %v", seat.ID, seat.Cards)
		}
	}

	r := RehydrateRoom(s)
	if r.GameTable == nil {
		t.Fatal("expected table to be rehydrated")
	}
	if r.Id != s.Id || r.Opts != s.Opts || r.Version != s.Version {
		t.Fatalf("scalar fields lost: %+v", r)
	}

	// The whole table state, including seats' hole cards, community cards,
	// the remaining deck, indices, pot, owed amount and result, must survive.
	if got := r.GameTable.State(); !reflect.DeepEqual(got, gs) {
		t.Fatalf("table state not preserved\n got: %+v\nwant: %+v", got, gs)
	}

	// The active player must be the same seat, not merely an equal copy at index 0.
	if got, want := r.GameTable.Active().ID, src.GameTable.Active().ID; got != want {
		t.Fatalf("active player = %s want %s", got, want)
	}
	// And that pointer must be one of the rehydrated seats so that actions mutate table state.
	active := r.GameTable.Active()
	found := false
	for _, seat := range r.GameTable.Seats() {
		if seat.ID == active.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("active player not among rehydrated seats")
	}

	// Rehydrated table must be playable and behave like the original.
	wantLegal := src.GameTable.LegalActions()
	if got := r.GameTable.LegalActions(); !reflect.DeepEqual(got, wantLegal) {
		t.Fatalf("legal actions = %v want %v", got, wantLegal)
	}
	if _, err := r.GameTable.Check(); err != nil {
		t.Fatalf("rehydrated table not playable: %v", err)
	}
	if _, err := src.GameTable.Check(); err != nil {
		t.Fatalf("source table: %v", err)
	}
	if got, want := r.GameTable.State(), src.GameTable.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("tables diverged after identical action\n got: %+v\nwant: %+v", got, want)
	}
}

func TestRehydrateRoomPlayerMap(t *testing.T) {
	full := func(id string, pos int) Player {
		return Player{Id: id, TablePos: pos, Ready: true, SittingOut: true, Broke: true, ConnectionId: "conn-" + id}
	}
	s := State{Id: "x", Opts: testOpts, PlayerMap: map[string]Player{"a": full("a", 0), "b": full("b", 1)}}
	r := RehydrateRoom(s)
	if len(r.PlayerMap) != 2 {
		t.Fatalf("players = %d want 2", len(r.PlayerMap))
	}
	for id, want := range s.PlayerMap {
		got := r.PlayerMap[id]
		if got == nil || *got != want {
			t.Fatalf("player %s = %+v want %+v", id, got, want)
		}
	}
	if r.PlayerMap["a"] == r.PlayerMap["b"] {
		t.Fatal("players must not share a pointer")
	}
	// Mutating the rehydrated room must not write through to the State it came from.
	r.PlayerMap["a"].Ready = false
	if !s.PlayerMap["a"].Ready {
		t.Fatal("rehydrated players alias the source State")
	}
}

func TestStateRoundTrip(t *testing.T) {
	r := NewRoom("id", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", TablePos: 0, Ready: true}
	s := r.State()
	if s.Id != "id" || s.PlayerMap["a"].Ready != true || s.GameTableState.Seats != nil {
		t.Fatalf("unexpected state: %+v", s)
	}
	// State must be a copy, not aliasing the room's players
	r.PlayerMap["a"].Ready = false
	if !s.PlayerMap["a"].Ready {
		t.Fatal("State should copy player values")
	}
}

func TestStateCapturesEveryRoomField(t *testing.T) {
	r := newSeatedRoom(t, "a", "b", "c")
	advanceToFlop(t, r)
	r.Version = 42
	for i, id := range []string{"a", "b", "c"} {
		r.PlayerMap[id] = &Player{
			Id: id, TablePos: i, Ready: true, SittingOut: i == 1, Broke: i == 2,
			ConnectionId: "conn-" + id,
		}
	}

	s := r.State()
	if s.Id != r.Id || s.Opts != r.Opts || s.Version != r.Version {
		t.Fatalf("scalar fields not captured: %+v", s)
	}
	if len(s.PlayerMap) != len(r.PlayerMap) {
		t.Fatalf("players = %d want %d", len(s.PlayerMap), len(r.PlayerMap))
	}
	for id, p := range r.PlayerMap {
		if got := s.PlayerMap[id]; got != *p {
			t.Fatalf("player %s = %+v want %+v", id, got, *p)
		}
	}
	if want := r.GameTable.State(); !reflect.DeepEqual(s.GameTableState, want) {
		t.Fatalf("table state not captured\n got: %+v\nwant: %+v", s.GameTableState, want)
	}

	// Every field of Room other than the live table pointer and PlayerMap must
	// have a counterpart in State, so a field added to Room can't be forgotten.
	st := reflect.TypeOf(State{})
	for i := 0; i < reflect.TypeOf(Room{}).NumField(); i++ {
		name := reflect.TypeOf(Room{}).Field(i).Name
		if name == "GameTable" {
			name = "GameTableState"
		}
		if _, ok := st.FieldByName(name); !ok {
			t.Fatalf("Room.%s has no counterpart in State", name)
		}
	}
}

func TestStateDoesNotAliasTable(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	s := r.State()
	snapshot := r.State() // independent second snapshot to compare against

	// Mutate the live table; the earlier State must be unaffected.
	if _, err := r.GameTable.Call(); err != nil {
		t.Fatalf("call: %v", err)
	}
	if !reflect.DeepEqual(s.GameTableState, snapshot.GameTableState) {
		t.Fatal("State changed after the table advanced")
	}
	// Mutating the State's slices must not reach the live table either.
	s.GameTableState.Seats[0].Cards[0] = s.GameTableState.Seats[0].Cards[1]
	s.GameTableState.Deck = s.GameTableState.Deck[:0]
	if got := r.GameTable.State(); len(got.Deck) == 0 {
		t.Fatal("mutating State affected the live table deck")
	}
}

func TestStateAndRehydrateRoundTrip(t *testing.T) {
	src := newSeatedRoom(t, "a", "b", "c")
	advanceToFlop(t, src)
	src.Version = 3
	first := src.State()
	second := RehydrateRoom(first).State()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("State -> Rehydrate -> State is not stable\nfirst:  %+v\nsecond: %+v", first, second)
	}
}

func TestGetPlayerIds(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["b"] = &Player{Id: "b", TablePos: 1}
	r.PlayerMap["a"] = &Player{Id: "a", TablePos: 0}
	ids := r.GetPlayerIds()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("GetPlayerIds = %v", ids)
	}
}

func TestPlayersAreReady(t *testing.T) {
	tests := []struct {
		name    string
		players []Player
		want    bool
	}{
		{"too few players", []Player{{Id: "a", Ready: true}}, false},
		{"not all ready", []Player{{Id: "a", Ready: true}, {Id: "b"}}, false},
		{"all ready", []Player{{Id: "a", Ready: true}, {Id: "b", Ready: true}}, true},
		{"sitting out counts as settled", []Player{
			{Id: "a", Ready: true}, {Id: "b", SittingOut: true}, {Id: "c", Ready: true}}, true},
		{"broke counts as settled", []Player{
			{Id: "a", Ready: true}, {Id: "b", Broke: true}, {Id: "c", Ready: true}}, true},
		{"only one active player", []Player{
			{Id: "a", Ready: true}, {Id: "b", Broke: true}, {Id: "c", SittingOut: true}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRoom("r", testOpts)
			for i := range tt.players {
				p := tt.players[i]
				p.TablePos = i
				r.PlayerMap[p.Id] = &p
			}
			if got := r.PlayersAreReady(); got != tt.want {
				t.Fatalf("PlayersAreReady() = %v want %v", got, tt.want)
			}
		})
	}
}

func TestResetPlayersReadyMarksBroke(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", Ready: true}
	r.PlayerMap["b"] = &Player{Id: "b", Ready: true, Broke: false}
	r.ResetPlayersReady()
	for id, p := range r.PlayerMap {
		if p.Ready {
			t.Fatalf("%s still ready", id)
		}
		// No table means no chips, so every player is broke
		if !p.Broke {
			t.Fatalf("%s should be broke without chips", id)
		}
	}
}

func TestResetPlayersReadyWithChips(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	r.ResetPlayersReady()
	for id, p := range r.PlayerMap {
		if p.Ready {
			t.Fatalf("%s still ready", id)
		}
		chips := r.GetPlayerState(id).Chips
		if p.Broke != (chips == 0) {
			t.Fatalf("%s broke=%v with %d chips", id, p.Broke, chips)
		}
	}
}

func TestGetPlayersSittingOut(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", SittingOut: true}
	r.PlayerMap["b"] = &Player{Id: "b"}
	out := r.GetPlayersSittingOut()
	if len(out) != 1 || out[0] != "a" {
		t.Fatalf("GetPlayersSittingOut = %v", out)
	}
}

func TestGetPlayerState(t *testing.T) {
	r := NewRoom("r", testOpts)
	if got := r.GetPlayerState("a"); got.ID != "" {
		t.Fatalf("expected empty state without table, got %+v", got)
	}
	r = newSeatedRoom(t, "a", "b")
	if got := r.GetPlayerState("a"); got.ID != "a" {
		t.Fatalf("GetPlayerState(a) = %+v", got)
	}
	if got := r.GetPlayerState("nobody"); got.ID != "" {
		t.Fatalf("expected empty state for unknown player, got %+v", got)
	}
}

func TestGetPlayerByConnectionId(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", ConnectionId: "c1"}
	if p := r.GetPlayerByConnectionId("c1"); p == nil || p.Id != "a" {
		t.Fatalf("expected player a, got %v", p)
	}
	if p := r.GetPlayerByConnectionId("c2"); p != nil {
		t.Fatalf("expected nil for unknown connection, got %v", p)
	}
}

func TestPlayerIsContestingAndObfuscation(t *testing.T) {
	// Build a finished showdown with two contestants, where the first player is
	// a winner and the second folded.
	cards := hand.Cards()
	tableCards := cards[0:5]
	ps := []table.Player{
		{ID: "a", Chips: 100, Cards: cards[5:7]},
		{ID: "b", Chips: 90, Cards: cards[7:9], Folded: true},
		{ID: "c", Chips: 0, Cards: cards[9:11], SittingOut: true},
		{ID: "d", Chips: 10, Cards: cards[11:13]},
	}
	s := table.State{
		Seats:  ps,
		Status: table.Done,
		Cards:  tableCards,
		Result: table.Result{
			Winners:     []table.Player{ps[0]},
			Contestants: []table.Player{ps[0], ps[3]},
			TableCards:  tableCards,
		},
	}
	r := &Room{Id: "r", PlayerMap: map[string]*Player{}, GameTable: table.NewFromState(nil, s)}
	for _, p := range ps {
		r.PlayerMap[p.ID] = &Player{Id: p.ID}
	}

	if !r.PlayerIsContesting("a") || r.PlayerIsContesting("b") || r.PlayerIsContesting("zz") {
		t.Fatal("PlayerIsContesting returned unexpected result")
	}

	obf := r.GetObfuscatedTableState()
	if obf.Deck != nil {
		t.Fatal("deck must be hidden")
	}
	if len(obf.Seats) != 4 {
		t.Fatalf("seats = %d", len(obf.Seats))
	}
	// a: contesting at showdown, cards revealed
	if len(obf.Seats[0].Cards) != 2 {
		t.Fatalf("contesting player cards not revealed: %v", obf.Seats[0].Cards)
	}
	// b: folded, cards hidden as empty slice
	if obf.Seats[1].Cards == nil || len(obf.Seats[1].Cards) != 0 {
		t.Fatalf("folded player should have empty non-nil cards: %v", obf.Seats[1].Cards)
	}
	// c: sitting out, cards hidden as empty slice
	if obf.Seats[2].Cards == nil || len(obf.Seats[2].Cards) != 0 {
		t.Fatalf("sitting-out player should have empty non-nil cards: %v", obf.Seats[2].Cards)
	}
	// d: contesting, cards revealed
	if len(obf.Seats[3].Cards) != 2 {
		t.Fatalf("second contestant cards not revealed: %v", obf.Seats[3].Cards)
	}
}

func TestObfuscationDuringPlay(t *testing.T) {
	cards := hand.Cards()
	ps := []table.Player{
		{ID: "a", Chips: 90, ChipsInPot: 10, Cards: cards[0:2]},
		{ID: "b", Chips: 80, ChipsInPot: 20, Cards: cards[2:4]},
	}
	s := table.State{Seats: ps, Status: table.Dealing, Deck: cards[4:]}
	r := &Room{Id: "r", PlayerMap: map[string]*Player{}, GameTable: table.NewFromState(nil, s)}
	obf := r.GetObfuscatedTableState()
	// Chips in pot are visible mid-hand, cards are not
	if obf.Seats[0].ChipsInPot != 10 || obf.Seats[0].Cards != nil {
		t.Fatalf("mid-hand obfuscation wrong: %+v", obf.Seats[0])
	}
}

func TestGetResultStr(t *testing.T) {
	cards := hand.Cards()
	tableCards := cards[0:5]
	winner := table.Player{ID: "a", Cards: cards[5:7]}
	loser := table.Player{ID: "b", Cards: cards[7:9]}

	mk := func(res table.Result) *Room {
		return &Room{GameTable: table.NewFromState(nil, table.State{Result: res})}
	}

	// Nil result fields -> no result yet
	if s := mk(table.Result{}).GetResultStr(); s != "" {
		t.Fatalf("empty result should yield empty string, got %q", s)
	}

	// Single contestant -> won on a fold
	if s := mk(table.Result{
		Winners:     []table.Player{winner},
		Contestants: []table.Player{winner},
		TableCards:  []hand.Card{},
	}).GetResultStr(); s != "a wins." {
		t.Fatalf("fold win string = %q", s)
	}

	// Single winner at showdown
	res := table.Result{
		Winners:     []table.Player{winner},
		Contestants: []table.Player{winner, loser},
		TableCards:  tableCards,
	}
	if s := mk(res).GetResultStr(); len(s) == 0 || !strings.HasPrefix(s, "a wins with") {
		t.Fatalf("showdown string = %q", s)
	}

	// Split pot between two winners
	split := table.Result{
		Winners:     []table.Player{winner, loser},
		Contestants: []table.Player{winner, loser},
		TableCards:  tableCards,
	}
	if s := mk(split).GetResultStr(); len(s) == 0 || !strings.Contains(s, "split the pot") {
		t.Fatalf("split string = %q", s)
	}
}

func TestHandleHello(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a"}
	s, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "a", Type: messaging.MessageTypeHello})
	if err != nil || s.Id != "r" {
		t.Fatalf("hello: %v %+v", err, s)
	}
}

func TestHandleReadyNoTableNotEnoughPlayers(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", TablePos: 0}
	s, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "a", Type: messaging.MessageTypeReady})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PlayerMap["a"].Ready || r.PlayerMap["a"].SittingOut {
		t.Fatalf("player state wrong: %+v", r.PlayerMap["a"])
	}
	if r.GameTable != nil || s.GameTableState.Seats != nil {
		t.Fatal("table should not start with a single player")
	}
}

func TestHandleSitOutBeforeTable(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", TablePos: 0, Ready: true}
	r.PlayerMap["b"] = &Player{Id: "b", TablePos: 1, Ready: true}
	// Player b sits out before the table is created, so only a is active
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "b", Type: messaging.MessageTypeSitOut})
	if err != nil {
		t.Fatal(err)
	}
	if !r.PlayerMap["b"].SittingOut || r.PlayerMap["b"].Ready {
		t.Fatalf("b should be sitting out: %+v", r.PlayerMap["b"])
	}
	if r.GameTable != nil {
		t.Fatal("no table should start with only one active player")
	}
}

func TestHandleReadyStartsTable(t *testing.T) {
	r := newSeatedRoom(t, "a", "b", "c")
	if r.GameTable.State().Status == table.Broken {
		t.Fatal("table should be running")
	}
}

func TestHandleReadySeatedAndUnseatedPlayers(t *testing.T) {
	// Start a table with a and b; c joins later and is added through AddPlayer.
	r := newSeatedRoom(t, "a", "b")
	r.PlayerMap["c"] = &Player{Id: "c", TablePos: 2}
	if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "c", Type: messaging.MessageTypeReady}); err != nil {
		t.Fatal(err)
	}
	if r.GetPlayerState("c").ID != "c" {
		t.Fatal("c should have been added to the table")
	}

	// a already seated: SetPlayerDefaulting path. Sitting out should not error.
	if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "a", Type: messaging.MessageTypeSitOut}); err != nil {
		t.Fatal(err)
	}
	if !r.PlayerMap["a"].SittingOut {
		t.Fatal("a should be sitting out")
	}
}

func TestHandleBuyInNoTable(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a", Broke: true}
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "a", Type: messaging.MessageTypeBuyIn})
	if err != nil {
		t.Fatal(err)
	}
	// Without a table the buy-in is a no-op
	if !r.PlayerMap["a"].Broke {
		t.Fatal("buy-in without table should not clear broke")
	}
}

func TestHandleBuyInWithTable(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	r.PlayerMap["a"].Broke = true
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "a", Type: messaging.MessageTypeBuyIn})
	if err != nil {
		t.Fatal(err)
	}
	if r.PlayerMap["a"].Broke {
		t.Fatal("buy-in should clear broke")
	}
	if !r.PlayerMap["a"].Ready {
		t.Fatal("buy-in should mark player ready")
	}
}

func TestHandleBuyInUnknownSeatLogsAndContinues(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	// Player is in the room but was never seated at the table; BuyPlayerIn errors
	// and the handler logs it, then continues.
	r.PlayerMap["ghost"] = &Player{Id: "ghost", Broke: true}
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{PlayerId: "ghost", Type: messaging.MessageTypeBuyIn})
	if err != nil {
		t.Fatal(err)
	}
	if r.PlayerMap["ghost"].Broke {
		t.Fatal("broke flag should still be cleared")
	}
}

func TestHandlePlayerActionNotTheirTurn(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	other := "a"
	if activeID(r) == "a" {
		other = "b"
	}
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
		PlayerId: other, Type: messaging.MessageTypePlayerAction, Action: table.Action{Type: table.Fold},
	})
	if err != table.ErrIllegalAction {
		// If the other player happened to be active, skip; otherwise must error
		if other == activeID(r) {
			t.Skip("unexpected active state")
		}
		t.Fatalf("expected ErrIllegalAction, got %v", err)
	}
}

func TestHandlePlayerActionIllegal(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	active := activeID(r)
	// Attempt an action the table rejects: bet below the big blind
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
		PlayerId: active, Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Bet, Chips: 1},
	})
	if err == nil {
		t.Fatal("expected error for illegal bet")
	}
}

// playToDone plays check/call actions until the one-shot hand reaches Done.
func playToDone(t *testing.T, r *Room) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if r.GameTable.State().Status == table.Done {
			return
		}
		_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
			PlayerId: activeID(r), Type: messaging.MessageTypePlayerAction,
			Action: legalAction(r.GameTable),
		})
		if err != nil {
			t.Fatalf("legal action rejected: %v", err)
		}
	}
	t.Fatal("hand did not finish")
}

func TestHandlePlayerActionFoldMarksFolded(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	folder := activeID(r)
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
		PlayerId: folder, Type: messaging.MessageTypePlayerAction,
		Action: table.Action{Type: table.Fold},
	})
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	for _, seat := range r.GameTable.Seats() {
		if seat.ID == folder && !seat.Folded {
			t.Fatalf("%s should be marked folded", folder)
		}
	}
}

func TestHandleReadyStartsNewRound(t *testing.T) {
	r := newSeatedRoom(t, "a", "b")
	playToDone(t, r)
	if r.GameTable.State().Status != table.Done {
		t.Fatal("expected hand to be done before starting a new round")
	}
	// Both players ready again: should call NewRound on the existing table
	for _, id := range []string{"a", "b"} {
		if _, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
			PlayerId: id, Type: messaging.MessageTypeReady,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if r.GameTable.State().Status != table.Dealing {
		t.Fatalf("expected new round to be dealing, got %v", r.GameTable.State().Status)
	}
}

func TestHandleInvalidMessageType(t *testing.T) {
	r := NewRoom("r", testOpts)
	r.PlayerMap["a"] = &Player{Id: "a"}
	_, err := r.HandleMessageFromPlayer(messaging.FromPlayerMessage{
		PlayerId: "a", Type: messaging.MessageTypeUnknown,
	})
	if err == nil {
		t.Fatal("expected error for unknown message type")
	}
}

func TestPlayerStatesAfterFullHandAreConsistent(t *testing.T) {
	// Play many random heads-up hands to exercise showdown paths.
	for range 25 {
		r := newSeatedRoom(t, "a", "b")
		playToDone(t, r)
		if r.GetResultStr() == "" {
			t.Fatal("finished hand should have a result string")
		}
		r.ResetPlayersReady()
		for id, p := range r.PlayerMap {
			if p.Broke != (r.GetPlayerState(id).Chips == 0) {
				t.Fatalf("%s broke flag inconsistent with chips", id)
			}
		}
	}
}

// legalAction chooses a simple action that is always legal for the active player.
func legalAction(tb *table.Table) table.Action {
	for _, a := range tb.LegalActions() {
		switch a {
		case table.Check:
			return table.Action{Type: table.Check}
		case table.Call:
			return table.Action{Type: table.Call}
		}
	}
	return table.Action{Type: table.Fold}
}
