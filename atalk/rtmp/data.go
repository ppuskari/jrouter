package rtmp

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// DataPacket represents an RTMP Data packet.
type DataPacket struct {
	RouterAddr    ddp.Addr
	Extended      bool
	NetworkTuples []NetworkTuple
}

// NetworkTuple represents routing information.
type NetworkTuple struct {
	Extended   bool
	RangeStart ddp.Network
	RangeEnd   ddp.Network
	Distance   uint8
}

// Marshal marshals an RTMP Data packet.
func (dp *DataPacket) Marshal() ([]byte, error) {
	b := bytes.NewBuffer(nil)
	write16(b, dp.RouterAddr.Network)
	b.WriteByte(8)
	b.WriteByte(byte(dp.RouterAddr.Node))
	if !dp.Extended {
		write16(b, uint16(0))
		b.WriteByte(0x82)
	}
	for _, nt := range dp.NetworkTuples {
		write16(b, nt.RangeStart)
		if !nt.Extended {
			b.WriteByte(nt.Distance)
			continue
		}
		b.WriteByte(nt.Distance | 0x80)
		write16(b, nt.RangeEnd)
		b.WriteByte(0x82)
	}
	return b.Bytes(), nil
}

// UnmarshalDataPacket unmarshals a DataPacket.
func UnmarshalDataPacket(data []byte) (*DataPacket, error) {
	if len(data) < 7 || (len(data)-4)%3 != 0 {
		return nil, fmt.Errorf("invalid input length %d for RTMP Data packet", len(data))
	}
	if data[2] != 8 {
		return nil, fmt.Errorf("unsupported node ID length %d for RTMP Data packet", data[2])
	}
	dp := &DataPacket{
		RouterAddr: ddp.Addr{
			Network: ddp.Network(binary.BigEndian.Uint16(data[:2])),
			Node:    ddp.Node(data[3]),
		},
		Extended: true,
	}
	data = data[4:]

	first := true
	for len(data) > 0 {
		if len(data) < 3 {
			return nil, fmt.Errorf("insufficient remaining input length %d for RTMP Data network tuple", len(data))
		}
		nt := NetworkTuple{
			RangeStart: ddp.Network(binary.BigEndian.Uint16(data[:2])),
			Distance:   data[2],
		}
		data = data[3:]
		if nt.RangeStart == 0 {
			// if non-extended, first tuple should contain version
			if !first {
				return nil, fmt.Errorf("invalid RTMP network tuple range start 0")
			}
			// initial non-extended tuple with Distance field containing version
			if nt.Distance != 0x82 {
				return nil, fmt.Errorf("unsupported RTMP version %x", nt.Distance)
			}
			dp.Extended = false
			first = false
			continue
		}
		nt.Extended = nt.Distance&0x80 != 0
		if !nt.Extended {
			// ordinary non-extended tuple
			if first && nt.RangeStart != 0 {
				return nil, fmt.Errorf("first RTMP network tuple is not version tuple")
			}
			dp.NetworkTuples = append(dp.NetworkTuples, nt)
			continue
		}

		// extended tuple
		if len(data) < 3 {
			return nil, fmt.Errorf("insufficient remaining input length %d for RTMP Data extended network tuple", len(data))
		}
		nt.Distance &^= 0x80
		nt.RangeEnd = ddp.Network(binary.BigEndian.Uint16(data[:2]))
		if first {
			if data[2] != 0x82 {
				return nil, fmt.Errorf("unsupported RTMP version %x", data[2])
			}
		}
		first = false
		dp.NetworkTuples = append(dp.NetworkTuples, nt)
		data = data[3:]
	}

	return dp, nil
}
