/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"maps"
	"slices"
)

// Set is a generic set.
// Yep, yet another set implementation. Took me 2 minutes to write *shrug*
type Set[K comparable] map[K]struct{}

func MakeSet[K comparable](ss ...K) Set[K] {
	set := make(Set[K], len(ss))
	set.Insert(ss...)
	return set
}

func (set Set[K]) ToSlice() []K {
	return slices.Collect(maps.Keys(set))
}

func (set Set[K]) Contains(s K) bool {
	_, c := set[s]
	return c
}

func (set Set[K]) Insert(ss ...K) {
	for _, s := range ss {
		set[s] = struct{}{}
	}
}

func (set Set[K]) Add(t Set[K]) {
	maps.Copy(set, t)
}

// nilToZero returns the zero value for T if a is nil, otherwise it type-asserts
// a as T.
func nilToZero[T any](a any) T {
	if a == nil {
		var zero T
		return zero
	}
	return a.(T)
}
