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

// Transport tracks local and remote domain identifiers, connection IDs, and
// sequence numbers for use in a pair of one-way connections.
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

// NewRIReqPacket returns a new RI-Req packet structure. By default it sets all
// SUI flags.
func (tr *Transport) NewRIReqPacket() *RIReqPacket {
	return &RIReqPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeRIReq,
			Flags:       RoutingFlagAllSUI,
		},
	}
}

// NewRIRspPacket returs a new RI-Rsp packet structure.
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

// NewRIAckPacket returns a new RI-Ack packet structure.
func (tr *Transport) NewRIAckPacket(connID, seq uint16, szi RoutingFlag) *RIAckPacket {
	return &RIAckPacket{
		Header: Header{
			TrHeader:    tr.sequenced(connID, seq),
			CommandCode: CmdCodeRIAck,
			Flags:       szi,
		},
	}
}

// NewZIRspPacket returns a new ZI-Rsp packet structure containing the given
// zone information. It automatically chooses between subcodes 1 or 2 depending
// on whether there is one network ID or more than one network ID.
func (tr *Transport) NewZIRspPacket(zones ZoneTuples) *ZIRspPacket {
	// Only one zone: use non-extended
	subcode := SubcodeZoneInfoNonExt
	if len(zones) > 1 {
		// Count distinct networks
		nns := make(map[uint16]struct{})
		for _, z := range zones {
			nns[z.Network] = struct{}{}
		}

		// Only one network: use extended format
		// More than one network: use non-extended
		if len(nns) == 1 {
			subcode = SubcodeZoneInfoExt
		}
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

// NewGDZLReqPacket returns a new GDZL-Req packet structure.
func (tr *Transport) NewGDZLReqPacket(startIdx uint16) *GDZLReqPacket {
	return &GDZLReqPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeZoneReq,
			Flags:       0,
		},
		Subcode:    SubcodeGetDomainZoneList,
		StartIndex: startIdx,
	}
}

// NewGZNRspPacket returns a new GDZL-Rsp packet structure. If GDZL function is
// not supported, startIdx should be set to -1.
func (tr *Transport) NewGDZLRspPacket(startIdx int16, zoneNames []string) *GDZLRspPacket {
	return &GDZLRspPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeZoneReq,
			Flags:       0,
		},
		Subcode:    SubcodeGetDomainZoneList,
		StartIndex: startIdx,
		ZoneNames:  zoneNames,
	}
}

// NewGZNReqPacket returns a new GZN-Req packet structure requesting nets for a
// given zone name.
func (tr *Transport) NewGZNReqPacket(zoneName string) *GZNReqPacket {
	return &GZNReqPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeZoneReq,
			Flags:       0,
		},
		Subcode:  SubcodeGetZonesNet,
		ZoneName: zoneName,
	}
}

// NewGZNRspPacket returns a new GZN-Rsp packet structure.
func (tr *Transport) NewGZNRspPacket(zoneName string, notSupported bool, nets NetworkTuples) *GZNRspPacket {
	return &GZNRspPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeZoneReq,
			Flags:       0,
		},
		Subcode:      SubcodeGetZonesNet,
		ZoneName:     zoneName,
		NotSupported: notSupported,
		Networks:     nets,
	}
}

// NewRDPacket returns a new RD packet structure.
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

// NewTicklePacket returns a new Tickle packet structure.
func (tr *Transport) NewTicklePacket() *TicklePacket {
	return &TicklePacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.LocalConnID),
			CommandCode: CmdCodeTickle,
			Flags:       0,
		},
	}
}

// NewTickleAckPacket returns a new Tickle-Ack packet.
func (tr *Transport) NewTickleAckPacket() *TickleAckPacket {
	return &TickleAckPacket{
		Header: Header{
			TrHeader:    tr.transaction(tr.RemoteConnID),
			CommandCode: CmdCodeTickleAck,
			Flags:       0,
		},
	}
}
