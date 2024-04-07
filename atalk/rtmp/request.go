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

package rtmp

import "fmt"

// RequestPacket represents an RTMP Request or RTMP Route Data Request packet.
type RequestPacket struct {
	Function uint8
}

// Marshal marshals an RTMP Request or RTMP RDR packet.
func (rp *RequestPacket) Marshal() ([]byte, error) {
	if rp.Function < 1 || rp.Function > 3 {
		return nil, fmt.Errorf("invalid RTMP request function %d", rp.Function)
	}
	return []byte{rp.Function}, nil
}

// UnmarshalRequestPacket unmarshals an RTMP Request or RTMP RDR packet.
func UnmarshalRequestPacket(data []byte) (*RequestPacket, error) {
	if len(data) != 1 {
		return nil, fmt.Errorf("invalid data length %d for RTMP Request or RTMP RDR packet", len(data))
	}
	return &RequestPacket{Function: data[0]}, nil
}
