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

package aep

import "fmt"

type Function uint8

const (
	EchoRequest Function = 1
	EchoReply   Function = 2
)

// Packet represents an AEP packet.
type Packet struct {
	Function Function
	Data     []byte
}

// Marshal marshals an AEP packet.
func (p *Packet) Marshal() ([]byte, error) {
	if p.Function < 1 || p.Function > 2 {
		return nil, fmt.Errorf("invalid AEP function %d", p.Function)
	}
	if len(p.Data) == 0 {
		return nil, fmt.Errorf("empty AEP packet")
	}
	return append([]byte{byte(p.Function)}, p.Data...), nil
}

// Unmarshal unmarshals an AEP packet.
func Unmarshal(data []byte) (*Packet, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("insufficient input length %d for AEP packet", len(data))
	}
	return &Packet{
		Function: Function(data[0]),
		Data:     data[1:],
	}, nil
}
