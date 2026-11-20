package random

import (
	"strings"
	"sync"
	"testing"
)

func TestConcurrencySafeSourceDeterministic(t *testing.T) {
	a := NewConcurrencySafeSource(1, 2)
	b := NewConcurrencySafeSource(1, 2)
	for range 10 {
		if a.Uint64() != b.Uint64() {
			t.Fatal("same seeds should produce identical sequences")
		}
	}
}

func TestConcurrencySafeSourceConcurrent(t *testing.T) {
	s := NewConcurrencySafeSource(3, 4)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Uint64()
		}()
	}
	wg.Wait()
}

func TestRandomString(t *testing.T) {
	for _, n := range []int{0, 1, 9, 64} {
		s := RandomString(n)
		if len(s) != n {
			t.Fatalf("RandomString(%d) length = %d", n, len(s))
		}
		for _, c := range s {
			if !strings.ContainsRune(charset, c) {
				t.Fatalf("unexpected character %q", c)
			}
		}
	}
}
