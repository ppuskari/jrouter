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

const (
	FunctionRequest         = 1
	FunctionRDRSplitHorizon = 2
	FunctionRDRComplete     = 3
	FunctionLoopProbe       = 4 // AURP Ch.4 pp 94
)

// RequestPacket represents an RTMP Request, Route Data Request, or Loop Probe.
type RequestPacket struct {
	Function uint8
	Data     []byte // only for LoopProbe
}

// Marshal marshals an RTMP Request or RTMP RDR packet.
func (rp *RequestPacket) Marshal() ([]byte, error) {
	if rp.Function < 1 || rp.Function > 4 {
		return nil, fmt.Errorf("invalid RTMP request function %d", rp.Function)
	}
	if rp.Function == FunctionLoopProbe {
		return append([]byte{rp.Function, 0x00, 0x00, 0x00, 0x00}, rp.Data...), nil
	}
	if len(rp.Data) > 0 {
		return nil, fmt.Errorf("data field only valid for RTMP Loop Probe")
	}
	return []byte{rp.Function}, nil
}

// UnmarshalRequestPacket unmarshals an RTMP Request or RTMP RDR packet.
func UnmarshalRequestPacket(data []byte) (*RequestPacket, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("invalid data length %d for RTMP request packet", len(data))
	}
	// Loop probes include four 0x00 bytes after the function.
	if data[0] == FunctionLoopProbe {
		if len(data) < 5 {
			return nil, fmt.Errorf("insufficient data length %d for RTMP Loop Probe", len(data))
		}
		return &RequestPacket{
			Function: data[0],
			Data:     data[5:],
		}, nil
	}
	return &RequestPacket{
		Function: data[0],
	}, nil
}
