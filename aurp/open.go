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

import (
	"encoding/binary"
	"fmt"
	"io"
)

// OpenReq is used to open a one-way connection between AIRs.
type OpenReqPacket struct {
	Header

	Version uint16 // currently always 1
	Options Options
}

func (p *OpenReqPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(p.Version)
	a.writeTo(p.Options)
	return a.ret()
}

func parseOpenReq(p []byte) (*OpenReqPacket, error) {
	if len(p) < 3 {
		return nil, fmt.Errorf("insufficient input length %d for Open-Req packet", len(p))
	}
	opts, err := parseOptions(p[2:])
	if err != nil {
		return nil, err
	}
	return &OpenReqPacket{
		Version: binary.BigEndian.Uint16(p[:2]),
		Options: opts,
	}, nil
}

// OpenRsp is used to respond to Open-Req.
type OpenRspPacket struct {
	Header

	RateOrErrCode int16
	Options       Options
}

func (p *OpenRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.RateOrErrCode))
	a.writeTo(p.Options)
	return a.ret()
}

func parseOpenRsp(p []byte) (*OpenRspPacket, error) {
	if len(p) < 3 {
		return nil, fmt.Errorf("insufficient input length %d for Open-Rsp packet", len(p))
	}
	opts, err := parseOptions(p[2:])
	if err != nil {
		return nil, err
	}
	return &OpenRspPacket{
		RateOrErrCode: int16(binary.BigEndian.Uint16(p[:2])),
		Options:       opts,
	}, nil
}

// OptionTuple is used to pass option information in Open-Req and Open-Rsp
// packets.
type OptionTuple struct {
	// Length uint8 = 1(for Type) + len(Data)
	Type OptionType
	Data []byte
}

func (ot *OptionTuple) WriteTo(w io.Writer) (int64, error) {
	if len(ot.Data) > 254 {
		return 0, fmt.Errorf("option tuple data too long [%d > 254]", len(ot.Data))
	}

	a := acc(w)
	a.write([]byte{
		byte(len(ot.Data) + 1),
		byte(ot.Type),
	})
	a.write(ot.Data)
	return a.ret()
}

func parseOptionTuple(p []byte) (OptionTuple, []byte, error) {
	if len(p) < 2 {
		return OptionTuple{}, p, fmt.Errorf("insufficient input length %d for option tuple", len(p))
	}
	olen := int(p[0]) + 1
	if len(p) < olen {
		return OptionTuple{}, p, fmt.Errorf("insufficient input for option tuple data length %d", olen)
	}
	return OptionTuple{
		Type: OptionType(p[1]),
		Data: p[2:olen],
	}, p[olen:], nil
}

// OptionType is used to distinguish different options.
type OptionType uint8

// Various option types
const (
	OptionTypeAuthentication OptionType = 0x01
	// All other types reserved
)

type Options []OptionTuple

func (o Options) WriteTo(w io.Writer) (int64, error) {
	if len(o) > 255 {
		return 0, fmt.Errorf("too many options [%d > 255]", len(o))
	}

	a := acc(w)
	a.write8(uint8(len(o)))
	for _, ot := range o {
		a.writeTo(&ot)
	}
	return a.ret()
}

func parseOptions(p []byte) (Options, error) {
	if len(p) < 1 {
		return nil, fmt.Errorf("insufficint input length %d for options", len(p))
	}
	optc := p[0]
	opts := make([]OptionTuple, optc)
	for i := range optc {
		ot, np, err := parseOptionTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing option %d: %w", i, err)
		}
		opts[i] = ot
		p = np
	}
	// TODO: warn about trailing data?
	return opts, nil
}
