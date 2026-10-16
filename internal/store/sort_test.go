package store

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"testing"
)

type testTaskSpec struct {
	Name     string `json:"name"`
	Priority any    `json:"priority,omitempty"`
	Tags     []any  `json:"tags,omitempty"`
}

type testTaskStatus struct {
	Status
}

func testTaskKind() *KindOf[testTaskSpec, testTaskStatus] {
	return Define[testTaskSpec, testTaskStatus](KindInfo{
		Name: "test.task", Prefix: "ttask", Scope: ScopeServer, Title: "Task", Plural: "tasks",
		Description: "A test task with a numeric index.",
		Indexes:     []Index{{Name: "priority", Path: "priority"}, {Name: "tag", Path: "tags"}},
	})
}

// forEachTaskStore runs a test against both stores holding test tasks.
func forEachTaskStore(t *testing.T, test func(t *testing.T, s Store)) {
	reg := NewKindRegistry()
	if err := reg.Register(testTaskKind()); err != nil {
		t.Fatal(err)
	}
	t.Run("memory", func(t *testing.T) { test(t, NewMemory(reg, WithClock(testClock()))) })
	t.Run("sqlite", func(t *testing.T) {
		s, err := NewSQLite(context.Background(), openTestDB(t), reg, WithClock(testClock()))
		if err != nil {
			t.Fatal(err)
		}
		test(t, s)
	})
}

func createTask(t *testing.T, s Store, spec string) ID {
	t.Helper()
	r := &Resource{Meta: Meta{Kind: "test.task", Scope: ServerScope()}, Spec: json.RawMessage(spec)}
	if err := write(context.Background(), s, func(tx Tx) error { return tx.Create(context.Background(), r) }); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// sorted lists every task in an order, two at a time so every cursor is used.
func sorted(t *testing.T, s Store, sort string) []ID {
	t.Helper()
	var got []ID
	cursor := ""
	for range 20 {
		page, err := s.List(context.Background(), Query{Kind: "test.task", Sort: sort, Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("sort %q: %v", sort, err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if cursor = page.Next; cursor == "" {
			break
		}
	}
	return got
}

// Numbers sort by their value, not as text, in every store and with cursors.
func TestSortingByNumbers(t *testing.T) {
	forEachTaskStore(t, func(t *testing.T, s Store) {
		nine := createTask(t, s, `{"name":"nine","priority":9}`)
		ten := createTask(t, s, `{"name":"ten","priority":10}`)
		hundred := createTask(t, s, `{"name":"hundred","priority":100}`)
		negative := createTask(t, s, `{"name":"negative","priority":-1.5}`)
		half := createTask(t, s, `{"name":"half","priority":0.5}`)
		zero := createTask(t, s, `{"name":"zero","priority":-0}`)
		text := createTask(t, s, `{"name":"text","priority":"high"}`)
		yes := createTask(t, s, `{"name":"yes","priority":true}`)
		none := createTask(t, s, `{"name":"none"}`)

		// Without a value first, then numbers, text and booleans.
		want := []ID{none, negative, zero, half, nine, ten, hundred, text, yes}
		if got := sorted(t, s, "priority"); !slices.Equal(got, want) {
			t.Errorf("by priority = %v, want %v", got, want)
		}
		slices.Reverse(want)
		if got := sorted(t, s, "-priority"); !slices.Equal(got, want) {
			t.Errorf("by priority, reversed = %v, want %v", got, want)
		}

		// A resource with several values sorts by its lowest.
		mixed := createTask(t, s, `{"name":"mixed","tags":[30, 4, "b"]}`)
		single := createTask(t, s, `{"name":"single","tags":[12]}`)
		word := createTask(t, s, `{"name":"word","tags":["a"]}`)
		got := sorted(t, s, "tag")
		var tagged []ID
		for _, id := range got {
			if id == mixed || id == single || id == word {
				tagged = append(tagged, id)
			}
		}
		if !slices.Equal(tagged, []ID{mixed, single, word}) {
			t.Errorf("by tag = %v", tagged)
		}

		// Filters still compare the values as they are written.
		page, err := s.List(context.Background(), Query{Kind: "test.task", Index: map[string]string{"priority": "10"}})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != ten {
			t.Errorf("filtering by 10 = %+v, %v", page, err)
		}
	})
}

// Values indexed before sort keys existed get theirs when the store opens.
func TestSortKeysAreFilledIn(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	reg := NewKindRegistry()
	if err := reg.Register(testTaskKind()); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLite(ctx, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	two := createTask(t, s, `{"name":"two","priority":2}`)
	eleven := createTask(t, s, `{"name":"eleven","priority":11}`)
	text := createTask(t, s, `{"name":"text","priority":"b"}`)
	// A resource of a kind this binary no longer knows keeps its values.
	if _, err := db.Exec(ctx, `INSERT INTO resources (id, kind, scope_level, spec, generation, revision, created_at, updated_at)
		VALUES ('gone_1', 'test.gone', 'server', '{}', 1, 1, '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO resource_index_values (resource_id, kind, name, value) VALUES ('gone_1', 'test.gone', 'priority', 'x')`); err != nil {
		t.Fatal(err)
	}
	// As they were before the migration that added the keys.
	if _, err := db.Exec(ctx, `UPDATE resource_index_values SET sort_key = ''`); err != nil {
		t.Fatal(err)
	}
	if s, err = NewSQLite(ctx, db, reg); err != nil {
		t.Fatal(err)
	}
	if got := sorted(t, s, "priority"); !slices.Equal(got, []ID{two, eleven, text}) {
		t.Errorf("after filling in = %v", got)
	}
	var unsorted int
	if err := db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM resource_index_values WHERE sort_key = ''`).Scan(&unsorted); err != nil || unsorted != 0 {
		t.Errorf("values left without a key = %d, %v", unsorted, err)
	}
	var key string
	if err := db.Reader().QueryRowContext(ctx, `SELECT sort_key FROM resource_index_values WHERE resource_id = 'gone_1'`).Scan(&key); err != nil || key != textSortKey("x") {
		t.Errorf("the key of an unknown kind's value = %q, %v", key, err)
	}
}

func TestNumberSortKeysOrderNumbers(t *testing.T) {
	numbers := []float64{-1e300, -1000, -2.5, -1, -0.001, 0, 1e-9, 0.5, 1, 2, 9, 10, 99.9, 100, 1e300}
	for i := 1; i < len(numbers); i++ {
		if numberSortKey(numbers[i-1]) >= numberSortKey(numbers[i]) {
			t.Errorf("%v doesn't sort before %v", numbers[i-1], numbers[i])
		}
	}
	if numberSortKey(0) != numberSortKey(math.Copysign(0, -1)) {
		t.Error("zero and negative zero sort the same")
	}
	if !(numberSortKey(1e300) < textSortKey("") && textSortKey("zzz") < boolSortKey(false) && boolSortKey(false) < boolSortKey(true)) {
		t.Error("numbers come before text, and text before booleans")
	}
}
