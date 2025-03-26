/*
   Copyright 2025 Josh Deprez

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

package aurp

import (
	"fmt"
	"strings"
)

func joinStringers[S ~[]E, E fmt.Stringer](s S, j string) string {
	var sb strings.Builder
	for i, x := range s {
		if i > 0 {
			sb.WriteString(j)
		}
		sb.WriteString(x.String())
	}
	return sb.String()
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
