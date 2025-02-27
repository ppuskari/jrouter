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
	"fmt"
	"maps"
	"slices"
	"time"
)

// StringSet is a set of strings.
// Yep, yet another string set implementation. Took me 2 minutes to write *shrug*
type StringSet map[string]struct{}

func (set StringSet) ToSlice() []string {
	return slices.Collect(maps.Keys(set))
}

func (set StringSet) Contains(s string) bool {
	_, c := set[s]
	return c
}

func (set StringSet) Insert(ss ...string) {
	for _, s := range ss {
		set[s] = struct{}{}
	}
}

func (set StringSet) Add(t StringSet) {
	for s := range t {
		set[s] = struct{}{}
	}
}

func SetFromSlice(ss []string) StringSet {
	set := make(StringSet, len(ss))
	set.Insert(ss...)
	return set
}

// ago is a helper for formatting times.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%v ago", time.Since(t).Truncate(time.Millisecond))
}
