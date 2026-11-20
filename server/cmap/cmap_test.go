package cmap

import (
	"maps"
	"slices"
	"sync"
	"testing"
)

func TestGetSetDelete(t *testing.T) {
	c := New[string, int]()
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected missing key on empty map")
	}
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get after Set = %v, %v; want 1, true", v, ok)
	}
	c.Set("a", 2)
	if v, _ := c.Get("a"); v != 2 {
		t.Fatalf("overwrite: got %v want 2", v)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d want 1", c.Len())
	}
	c.Delete("a")
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected key deleted")
	}
	c.Delete("missing") // deleting absent key is a no-op
	if c.Len() != 0 {
		t.Fatalf("Len = %d want 0", c.Len())
	}
}

func TestNewWithSizeHint(t *testing.T) {
	c := New[int, string](10)
	c.Set(1, "x")
	if c.Len() != 1 {
		t.Fatalf("Len = %d want 1", c.Len())
	}
}

func TestIterators(t *testing.T) {
	c := New[string, int](4)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	got := map[string]int{}
	for k, v := range c.All() {
		got[k] = v
	}
	if !maps.Equal(got, map[string]int{"a": 1, "b": 2, "c": 3}) {
		t.Fatalf("All() = %v", got)
	}

	keys := slices.Collect(c.Keys())
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"a", "b", "c"}) {
		t.Fatalf("Keys() = %v", keys)
	}

	vals := slices.Collect(c.Values())
	slices.Sort(vals)
	if !slices.Equal(vals, []int{1, 2, 3}) {
		t.Fatalf("Values() = %v", vals)
	}
}

func TestIteratorEarlyBreak(t *testing.T) {
	c := New[int, int]()
	for i := range 5 {
		c.Set(i, i)
	}
	n := 0
	for range c.All() {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("All() yielded %d items after break, want 1", n)
	}
	n = 0
	for range c.Keys() {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("Keys() yielded %d items after break, want 1", n)
	}
	n = 0
	for range c.Values() {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("Values() yielded %d items after break, want 1", n)
	}
}

func TestIteratorsSnapshot(t *testing.T) {
	c := New[int, int]()
	c.Set(1, 1)
	seq := c.All()
	c.Set(2, 2) // mutation after snapshot must not be visible
	n := 0
	for range seq {
		n++
	}
	if n != 1 {
		t.Fatalf("snapshot yielded %d items, want 1", n)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := New[int, int]()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Set(i, i)
			c.Get(i)
			c.Len()
			c.Delete(i)
			for range c.All() {
			}
		}()
	}
	wg.Wait()
}
