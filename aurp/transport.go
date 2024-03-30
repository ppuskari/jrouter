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

type Transport struct {
	// LocalDI and RemoteDI are used for producing packets.
	// When sending a packet, we use LocalDI as SourceDI and RemoteDI as
	// DestinationDI.
	// (When receiving a packet, we expect to see LocalDI as DestinationDI
	// - but it might not be - and we expect to see RemoteDI as SourceDI.)
	LocalDI, RemoteDI DomainIdentifier

	// LocalConnID is used for packets sent in the role of data receiver.
	// RemoteConnID is used for packets sent in the role of data sender.
	LocalConnID, RemoteConnID uint16

	// LocalSeq is used for packets sent (as data sender)
	// RemoteSeq is used to check packets received (remote is the data sender).
	LocalSeq, RemoteSeq uint16
}

func (tr *Transport) IncLocalSeq() {
	tr.LocalSeq++
	if tr.LocalSeq == 0 {
		tr.LocalSeq = 1
	}
}

func (tr *Transport) IncRemoteSeq() {
	tr.RemoteSeq++
	if tr.RemoteSeq == 0 {
		tr.RemoteSeq = 1
	}
}

// domainHeader returns a new domain header suitable for sending a packet.
func (tr *Transport) domainHeader(pt PacketType) DomainHeader {
	return DomainHeader{
		DestinationDI: tr.RemoteDI,
		SourceDI:      tr.LocalDI,
		Version:       1,
		Reserved:      0,
		PacketType:    pt,
	}
}

// transaction returns a new TrHeader, usable for transaction requests or
// responses. Both data senders and data receivers can send transactions.
// It should be given one of tr.LocalConnID (as data receiver) or
// tr.RemoteConnID (as data sender).
func (tr *Transport) transaction(connID uint16) TrHeader {
	return TrHeader{
		DomainHeader: tr.domainHeader(PacketTypeRouting),
		ConnectionID: connID,
		Sequence:     0, // Transaction packets all use sequence number 0.
	}
}

// sequenced returns a new TrHeader usable for sending a sequenced data packet.
// Only data senders send sequenced data.
func (tr *Transport) sequenced(connID, seq uint16) TrHeader {
	return TrHeader{
		DomainHeader: tr.domainHeader(PacketTypeRouting),
		ConnectionID: connID,
		Sequence:     seq,
	}
}

// NewOpenReqPacket returns a new Open-Req packet structure. By default it sets
// all SUI flags and uses version 1.
func (tr *Transport) NewOpenReqPacket(opts Options) *OpenReqPacket {
	return &OpenReqPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeOpenReq,
			Flags:       RoutingFlagAllSUI,
		},
		Version: 1,
		Options: opts,
	}
}

// NewOpenRspPacket returns a new Open-Rsp packet structure.
func (tr *Transport) NewOpenRspPacket(envFlags RoutingFlag, rateOrErr int16, opts Options) *OpenRspPacket {
	return &OpenRspPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeOpenRsp,
			Flags:       envFlags,
		},
		RateOrErrCode: rateOrErr,
		Options:       opts,
	}
}

func (tr *Transport) NewRIRspPacket(last RoutingFlag, nets NetworkTuples) *RIRspPacket {
	return &RIRspPacket{
		Header: Header{
			TrHeader:    tr.sequenced(tr.RemoteConnID, tr.LocalSeq),
			CommandCode: CmdCodeRIRsp,
			Flags:       last,
		},
		Networks: nets,
	}
}

func (tr *Transport) NewRIAckPacket(connID, seq uint16, szi RoutingFlag) *RIAckPacket {
	return &RIAckPacket{
		Header: Header{
			TrHeader:    tr.sequenced(connID, seq),
			CommandCode: CmdCodeRIAck,
			Flags:       szi,
		},
	}
}

func (tr *Transport) NewZIRspPacket(zones ZoneTuples) *ZIRspPacket {
	nns := make(map[uint16]struct{})
	for _, z := range zones {
		nns[z.Network] = struct{}{}
	}
	// Only one network: use extended
	// More than one network: use non-extended
	subcode := SubcodeZoneInfoExt
	if len(nns) != 1 {
		subcode = SubcodeZoneInfoNonExt
	}

	return &ZIRspPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeZoneRsp,
			Flags:       0,
		},
		Subcode: subcode,
		Zones:   zones,
	}
}

func (tr *Transport) NewRDPacket(errCode ErrorCode) *RDPacket {
	return &RDPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeRD,
			Flags:       0,
		},
		ErrorCode: errCode,
	}
}

func (tr *Transport) NewTicklePacket() *TicklePacket {
	return &TicklePacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeTickle,
			Flags:       0,
		},
	}
}

func (tr *Transport) NewTickleAckPacket() *TickleAckPacket {
	return &TickleAckPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeTickleAck,
			Flags:       0,
		},
	}
}
