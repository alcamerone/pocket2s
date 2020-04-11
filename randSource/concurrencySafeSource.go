package randSource

import (
	"math/rand"
	"sync"
)

type ConcurrencySafeSource struct {
	r *rand.Rand
	m sync.Mutex
}

func NewConcurrencySafeSource(seed int64) *ConcurrencySafeSource {
	return &ConcurrencySafeSource{r: rand.New(rand.NewSource(seed))}
}

func (s *ConcurrencySafeSource) Int63() int64 {
	s.m.Lock()
	defer s.m.Unlock()
	return s.r.Int63()
}

func (s *ConcurrencySafeSource) Seed(seed int64) {
	s.m.Lock()
	defer s.m.Unlock()
	s.r.Seed(seed)
}
