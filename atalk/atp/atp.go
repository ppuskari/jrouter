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

package atp

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	ciFuncMask   = 0b11000000
	ciFuncTReq   = 0b01000000
	ciFuncTResp  = 0b10000000
	ciFuncTRel   = 0b11000000
	ciXOBit      = 0b00100000
	ciEOMBit     = 0b00010000
	ciSTSBit     = 0b00001000
	ciTRelTOMask = 0b00000111
)

type TReq struct {
	// Control information: 0 1 (XO) 0 0 (Timeout indicator)
	ExactlyOnce          bool
	TRelTimeoutIndicator uint8 // only if XO is set
	Bitmap               uint8
	TransactionID        uint16
	UserBytes            [4]byte
	Data                 []byte
}

func (p *TReq) Marshal() ([]byte, error) {
	if p.TRelTimeoutIndicator != 0 && !p.ExactlyOnce {
		return nil, fmt.Errorf("TRel timeout indicator %d only valid for XO TReq", p.TRelTimeoutIndicator)
	}
	if p.TRelTimeoutIndicator > 4 {
		return nil, fmt.Errorf("invalid TRel timeout indicator [%d > 4]", p.TRelTimeoutIndicator)
	}

	b := bytes.NewBuffer(nil)
	ci := byte(ciFuncTReq)
	if p.ExactlyOnce {
		ci |= ciXOBit
	}
	ci |= p.TRelTimeoutIndicator
	b.WriteByte(ci)
	b.WriteByte(p.Bitmap)
	write16(b, p.TransactionID)
	b.Write(p.UserBytes[:])
	b.Write(p.Data)
	return b.Bytes(), nil
}

type TResp struct {
	// Control information: 1 0 0 (EOM) (STS) 0 0 0
	EndOfMessage          bool
	SendTransactionStatus bool
	Sequence              uint8
	TransactionID         uint16
	UserBytes             [4]byte
	Data                  []byte
}

func (p *TResp) Marshal() ([]byte, error) {
	b := bytes.NewBuffer(nil)
	ci := byte(ciFuncTResp)
	if p.EndOfMessage {
		ci |= ciEOMBit
	}
	if p.SendTransactionStatus {
		ci |= ciSTSBit
	}
	b.WriteByte(ci)
	b.WriteByte(p.Sequence)
	write16(b, p.TransactionID)
	b.Write(p.UserBytes[:])
	b.Write(p.Data)
	return b.Bytes(), nil
}

type TRel struct {
	// Control information: 1 1 0 0 0 0 0 0
	TransactionID uint16
}

func (p *TRel) Marshal() ([]byte, error) {
	return []byte{
		ciFuncTRel, 0,
		byte(p.TransactionID >> 8),
		byte(p.TransactionID & 0xFF),
		0, 0, 0, 0,
	}, nil
}

func UnmarshalPacket(data []byte) (any, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("insufficient input length %d for ATP packet", len(data))
	}
	switch data[0] & ciFuncMask {
	case ciFuncTReq:
		return &TReq{
			ExactlyOnce:          data[0]&ciXOBit != 0,
			TRelTimeoutIndicator: data[0] & ciTRelTOMask,
			Bitmap:               data[1],
			TransactionID:        binary.BigEndian.Uint16(data[2:4]),
			UserBytes:            [4]byte(data[4:8]),
			Data:                 data[8:],
		}, nil

	case ciFuncTResp:
		return &TResp{
			EndOfMessage:          data[0]&ciEOMBit != 0,
			SendTransactionStatus: data[0]&ciSTSBit != 0,
			Sequence:              data[1],
			TransactionID:         binary.BigEndian.Uint16(data[2:4]),
			UserBytes:             [4]byte(data[4:8]),
			Data:                  data[8:],
		}, nil

	case ciFuncTRel:
		return &TRel{
			TransactionID: binary.BigEndian.Uint16(data[2:4]),
		}, nil

	default:
		return nil, fmt.Errorf("unknown ATP function in control information byte %b", data[0])
	}
}

func write16[I ~uint16](b *bytes.Buffer, n I) {
	b.Write([]byte{byte(n >> 8), byte(n & 0xff)})
}
