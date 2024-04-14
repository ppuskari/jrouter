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

package main

import (
	"fmt"

	"gitea.drjosh.dev/josh/jrouter/atalk/atp"
	"gitea.drjosh.dev/josh/jrouter/atalk/zip"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/aarp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

func handleZIP(pcapHandle *pcap.Handle, srcHWAddr, myHWAddr ethernet.Addr, myAddr aarp.AddrPair, cfg *config, zones *ZoneTable, ddpkt *ddp.ExtPacket) error {
	switch ddpkt.Proto {
	case 3: // ATP
		atpkt, err := atp.UnmarshalPacket(ddpkt.Data)
		if err != nil {
			return err
		}
		switch atpkt := atpkt.(type) {
		case *atp.TReq:
			gzl, err := zip.UnmarshalTReq(atpkt)
			if err != nil {
				return err
			}
			// TODO: handle this in a more transactiony way
			resp := &zip.GetZonesReplyPacket{
				TID:      gzl.TID,
				LastFlag: true, // TODO: support multiple response packets
			}
			switch gzl.Function {
			case zip.FunctionGetZoneList:
				resp.Zones = zones.AllNames()

			case zip.FunctionGetLocalZones:
				resp.Zones = zones.LocalNames()

			case zip.FunctionGetMyZone:
				return fmt.Errorf("TODO: support GetMyZone?")
			}

			respATP, err := resp.MarshalTResp()
			if err != nil {
				return err
			}
			ddpBody, err := respATP.Marshal()
			if err != nil {
				return err
			}
			respDDP := ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					DstNet:    ddpkt.SrcNet,
					DstNode:   ddpkt.SrcNode,
					DstSocket: ddpkt.SrcSocket,
					SrcNet:    myAddr.Proto.Network,
					SrcNode:   myAddr.Proto.Node,
					SrcSocket: 6,
					Proto:     ddp.ProtoATP,
				},
				Data: ddpBody,
			}
			outFrame, err := ethertalk.AppleTalk(myHWAddr, respDDP)
			if err != nil {
				return err
			}
			outFrame.Dst = srcHWAddr
			outFrameRaw, err := ethertalk.Marshal(*outFrame)
			if err != nil {
				return err
			}
			return pcapHandle.WritePacketData(outFrameRaw)

		case *atp.TResp:
			return fmt.Errorf("TODO: support handling ZIP ATP replies?")

		default:
			return fmt.Errorf("unsupported ATP packet type %T for ZIP", atpkt)
		}

	case 6: // ZIP
		zipkt, err := zip.UnmarshalPacket(ddpkt.Data)
		if err != nil {
			return err
		}

		var resp interface {
			Marshal() ([]byte, error)
		}

		switch zipkt := zipkt.(type) {
		case *zip.QueryPacket:
			// TODO: multiple packets
			resp = &zip.ReplyPacket{
				Extended: false,
				Networks: zones.Query(zipkt.Networks),
			}
			// TODO: direct to queryer

		case *zip.GetNetInfoPacket:
			// Only running a network with one zone for now.
			resp = &zip.GetNetInfoReplyPacket{
				ZoneInvalid:     zipkt.ZoneName != cfg.EtherTalk.ZoneName,
				UseBroadcast:    true, // TODO: add multicast addr computation
				OnlyOneZone:     true,
				NetStart:        cfg.EtherTalk.NetStart,
				NetEnd:          cfg.EtherTalk.NetEnd,
				ZoneName:        zipkt.ZoneName, // has to match request
				MulticastAddr:   ethertalk.AppleTalkBroadcast,
				DefaultZoneName: cfg.EtherTalk.ZoneName,
			}

		default:
			return fmt.Errorf("TODO: handle type %T", zipkt)
		}

		if resp == nil {
			return nil
		}
		respRaw, err := resp.Marshal()
		if err != nil {
			return fmt.Errorf("couldn't marshal %T: %w", resp, err)
		}

		// TODO: fix
		// "In cases where a node's provisional address is
		// invalid, routers will not be able to respond to
		// the node in a directed manner. An address is
		// invalid if the network number is neither in the
		// startup range nor in the network number range
		// assigned to the node's network. In these cases,
		// if the request was sent via a broadcast, the
		// routers should respond with a broadcast."
		ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket = 0x0000, 0xFF, ddpkt.SrcSocket
		ddpkt.SrcNet = myAddr.Proto.Network
		ddpkt.SrcNode = myAddr.Proto.Node
		ddpkt.SrcSocket = 6
		ddpkt.Data = respRaw
		outFrame, err := ethertalk.AppleTalk(myHWAddr, *ddpkt)
		if err != nil {
			return fmt.Errorf("couldn't create EtherTalk frame: %w", err)
		}
		outFrame.Dst = srcHWAddr
		outFrameRaw, err := ethertalk.Marshal(*outFrame)
		if err != nil {
			return fmt.Errorf("couldn't marshal EtherTalk frame: %w", err)
		}
		if err := pcapHandle.WritePacketData(outFrameRaw); err != nil {
			return fmt.Errorf("couldn't write packet data: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("invalid DDP type %d on socket 6", ddpkt.Proto)
	}
}
