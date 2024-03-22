package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// TrHeader represent an AURP-Tr packet header. It includes the domain header.
type TrHeader struct {
	DomainHeader

	ConnectionID uint16
	Sequence     uint16
}

// WriteTo writes the encoded form of the header to w, including the domain
// header.
func (h *TrHeader) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&h.DomainHeader)
	a.write16(h.ConnectionID)
	a.write16(h.Sequence)
	return a.ret()
}

func parseTrHeader(p []byte) (TrHeader, []byte, error) {
	if len(p) < 4 {
		return TrHeader{}, p, fmt.Errorf("insufficient input length %d for tr header", len(p))
	}
	return TrHeader{
		ConnectionID: binary.BigEndian.Uint16(p[:2]),
		Sequence:     binary.BigEndian.Uint16(p[2:4]),
	}, p[4:], nil
}

// incSequence increments the sequence number.
// Note that 0 is special and 65535+1 = 1, according to AURP.
func (tr *TrHeader) incSequence() {
	tr.Sequence++
	if tr.Sequence == 0 {
		tr.Sequence = 1
	}
}

// transaction returns a new TrHeader based on this one, usable for transaction
// requests or responses.
func (tr *TrHeader) transaction() TrHeader {
	return TrHeader{
		DomainHeader: DomainHeader{
			DestinationDI: tr.DestinationDI,
			SourceDI:      tr.SourceDI,
			Version:       1,
			Reserved:      0,
			PacketType:    PacketTypeRouting,
		},
		ConnectionID: tr.ConnectionID,
		Sequence:     0, // Transaction packets all use sequence number 0.
	}
}

// DataSender is used to track sender state in a one-way AURP connection.
// Note that both data senders and data recievers can send packets.
type DataSender struct {
	TrHeader
}

// NewOpenRspPacket returns a new Open-Rsp packet structure.
func (ds *DataSender) NewOpenRspPacket(envFlags RoutingFlag, rateOrErr int16, opts Options) *OpenRspPacket {
	return &OpenRspPacket{
		Header: Header{
			TrHeader:    ds.transaction(),
			CommandCode: CmdCodeOpenRsp,
			Flags:       envFlags,
		},
		RateOrErrCode: rateOrErr,
		Options:       opts,
	}
}

// DataReceiver is used to track reciever state in a one-way AURP connection.
// Note that both data senders and data recievers can send packets.
type DataReceiver struct {
	TrHeader
}

// NewOpenReqPacket returns a new Open-Req packet structure. By default it sets
// all SUI flags and uses version 1.
func (dr *DataReceiver) NewOpenReqPacket(opts Options) *OpenReqPacket {
	return &OpenReqPacket{
		Header: Header{
			TrHeader:    dr.transaction(),
			CommandCode: CmdCodeOpenReq,
			Flags:       RoutingFlagAllSUI,
		},
		Version: 1,
		Options: opts,
	}
}
