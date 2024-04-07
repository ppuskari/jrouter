package rtmp

import (
	"encoding/binary"
	"fmt"

	"github.com/sfiera/multitalk/pkg/ddp"
)

type ResponsePacket struct {
	SenderAddr ddp.Addr
	Extended   bool
	RangeStart ddp.Network
	RangeEnd   ddp.Network
}

func UnmarshalResponsePacket(data []byte) (*ResponsePacket, error) {
	if len(data) != 4 && len(data) != 10 {
		return nil, fmt.Errorf("invalid input length %d for RTMP Response packet", len(data))
	}
	if data[2] != 8 {
		return nil, fmt.Errorf("unsupported node ID length %d for RTMP Response packet", data[2])
	}
	rp := &ResponsePacket{
		SenderAddr: ddp.Addr{
			Network: ddp.Network(binary.BigEndian.Uint16(data[:2])),
			Node:    ddp.Node(data[3]),
		},
	}
	if len(data) == 4 {
		return rp, nil
	}

	rp.RangeStart = ddp.Network(binary.BigEndian.Uint16(data[4:6]))
	if data[6] != 0x80 {
		return nil, fmt.Errorf("invalid intermediate byte %x for RTMP Response packet", data[6])
	}
	rp.RangeEnd = ddp.Network(binary.BigEndian.Uint16(data[7:9]))
	if data[9] != 0x82 {
		return nil, fmt.Errorf("unsupported version %x for RTMP Response packet", data[9])
	}
	return rp, nil
}
