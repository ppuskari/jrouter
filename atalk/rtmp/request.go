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
