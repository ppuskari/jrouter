package rtmp

import "fmt"

// RequestPacket represents an RTMP Request or RTMP Route Data Request packet.
type RequestPacket struct {
	Function uint8
}

func UnmarshalRequestPacket(data []byte) (*RequestPacket, error) {
	if len(data) != 1 {
		return nil, fmt.Errorf("invalid data length %d for RTMP Request or RTMP RDR packet", len(data))
	}
	return &RequestPacket{Function: data[0]}, nil
}
