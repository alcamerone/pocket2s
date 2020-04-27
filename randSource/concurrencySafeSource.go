/*    package "randSource" provides random number sources for the Pocket2s server.
 *    Copyright (C) 2020 Cameron Ekblad.
 *    Email: al.camerone@gmail.com
 *
 *    This program is free software: you can redistribute it and/or modify
 *    it under the terms of the GNU Affero General Public License as published
 *    by the Free Software Foundation, either version 3 of the License, or
 *    (at your option) any later version.
 *
 *    This program is distributed in the hope that it will be useful,
 *    but WITHOUT ANY WARRANTY; without even the implied warranty of
 *    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *    GNU Affero General Public License for more details.
 *
 *    You should have received a copy of the GNU Affero General Public License
 *    along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

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
