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

package aurp

type ErrorCode int16

// Various error codes.
const (
	ErrCodeNormalClose           ErrorCode = -1
	ErrCodeRoutingLoop           ErrorCode = -2
	ErrCodeOutOfSync             ErrorCode = -3
	ErrCodeOptionNegotiation     ErrorCode = -4
	ErrCodeInvalidVersion        ErrorCode = -5
	ErrCodeInsufficientResources ErrorCode = -6
	ErrCodeAuthentication        ErrorCode = -7
)

func (e ErrorCode) String() string {
	return map[ErrorCode]string{
		ErrCodeNormalClose:           "normal connection close",
		ErrCodeRoutingLoop:           "routing loop detected",
		ErrCodeOutOfSync:             "connection out of sync",
		ErrCodeOptionNegotiation:     "option-negotiation error",
		ErrCodeInvalidVersion:        "invalid version number",
		ErrCodeInsufficientResources: "insufficient resources for connection",
		ErrCodeAuthentication:        "authentication error",
	}[e]
}
