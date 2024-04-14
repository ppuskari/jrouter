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

package zip

import (
	"bytes"
	"fmt"
)

type Function uint8

const (
	// ZIP packets
	FunctionQuery           = 1
	FunctionReply           = 2
	FunctionGetNetInfo      = 5
	FunctionGetNetInfoReply = 6
	FunctionNotify          = 7
	FunctionExtendedReply   = 8

	// ATP packets
	FunctionGetMyZone     = 7
	FunctionGetZoneList   = 8
	FunctionGetLocalZones = 9
)

// Non-ATP packets only
func UnmarshalPacket(data []byte) (any, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("insufficient input length %d for any ZIP packet", len(data))
	}
	switch data[0] {
	case FunctionQuery:
		return UnmarshalQueryPacket(data)

	case FunctionReply, FunctionExtendedReply:
		return UnmarshalReplyPacket(data)

	case FunctionGetNetInfo:
		return UnmarshalGetNetInfoPacket(data)

	case FunctionGetNetInfoReply:
		return nil, fmt.Errorf("ZIP GetNetInfo Reply unmarshaling unimplemented")

	case FunctionNotify:
		return nil, fmt.Errorf("ZIP Notify unmarshaling unimplemented")

	default:
		return nil, fmt.Errorf("unknown ZIP function %d", data[0])
	}
}

func write16[I ~uint16](b *bytes.Buffer, n I) {
	b.Write([]byte{byte(n >> 8), byte(n & 0xff)})
}
