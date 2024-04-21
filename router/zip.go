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

package router

import (
	"fmt"
	"log"

	"gitea.drjosh.dev/josh/jrouter/atalk"
	"gitea.drjosh.dev/josh/jrouter/atalk/atp"
	"gitea.drjosh.dev/josh/jrouter/atalk/zip"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

func (rtr *Router) HandleZIP(srcHWAddr ethernet.Addr, ddpkt *ddp.ExtPacket) error {
	switch ddpkt.Proto {
	case ddp.ProtoATP:
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
			if gzl.StartIndex == 0 {
				return fmt.Errorf("ZIP ATP: received request with StartIndex = 0 (invalid)")
			}

			resp := &zip.GetZonesReplyPacket{
				TID:      gzl.TID,
				LastFlag: true,
			}

			switch gzl.Function {
			case zip.FunctionGetZoneList:
				resp.Zones = rtr.ZoneTable.AllNames()

			case zip.FunctionGetLocalZones:
				resp.Zones = rtr.ZoneTable.LocalNames()

			case zip.FunctionGetMyZone:
				resp.Zones = []string{rtr.Config.EtherTalk.ZoneName}
			}

			// Inside AppleTalk SE, pp 8-8
			if int(gzl.StartIndex) > len(resp.Zones) {
				// "Note: A 0-byte response will be returned by a router if the
				// index specified in the request is greater than the index of
				// the last zone in the list (and the user bytes field will
				// indicate no more zones)."
				resp.Zones = nil
			} else {
				// Trim the zones list
				// "zone names in the router are assumed to be numbered starting
				// with 1"
				resp.Zones = resp.Zones[gzl.StartIndex-1:]
				size := 0
				for i, z := range resp.Zones {
					size += 1 + len(z) // length prefix plus string
					if size > atp.MaxDataSize {
						resp.LastFlag = false
						resp.Zones = resp.Zones[:i]
						break
					}
				}
			}

			respATP, err := resp.MarshalTResp()
			if err != nil {
				return err
			}
			ddpBody, err := respATP.Marshal()
			if err != nil {
				return err
			}
			respDDP := &ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(len(ddpBody)) + atalk.DDPExtHeaderSize,
					Cksum:     0,
					DstNet:    ddpkt.SrcNet,
					DstNode:   ddpkt.SrcNode,
					DstSocket: ddpkt.SrcSocket,
					SrcNet:    rtr.MyDDPAddr.Network,
					SrcNode:   rtr.MyDDPAddr.Node,
					SrcSocket: 6,
					Proto:     ddp.ProtoATP,
				},
				Data: ddpBody,
			}
			return rtr.SendEtherTalkDDP(srcHWAddr, respDDP)

		case *atp.TResp:
			return fmt.Errorf("TODO: support handling ZIP ATP replies?")

		default:
			return fmt.Errorf("unsupported ATP packet type %T for ZIP", atpkt)
		}

	case ddp.ProtoZIP:
		zipkt, err := zip.UnmarshalPacket(ddpkt.Data)
		if err != nil {
			return err
		}

		switch zipkt := zipkt.(type) {
		case *zip.QueryPacket:
			log.Printf("ZIP: Got Query for networks %v", zipkt.Networks)
			networks := rtr.ZoneTable.Query(zipkt.Networks)

			sendReply := func(resp *zip.ReplyPacket) error {
				respRaw, err := resp.Marshal()
				if err != nil {
					return fmt.Errorf("couldn't marshal %T: %w", resp, err)
				}
				outDDP := &ddp.ExtPacket{
					ExtHeader: ddp.ExtHeader{
						Size:      uint16(len(respRaw)) + atalk.DDPExtHeaderSize,
						Cksum:     0,
						DstNet:    ddpkt.SrcNet,
						DstNode:   ddpkt.SrcNode,
						DstSocket: ddpkt.SrcSocket,
						SrcNet:    rtr.MyDDPAddr.Network,
						SrcNode:   rtr.MyDDPAddr.Node,
						SrcSocket: 6,
						Proto:     ddp.ProtoZIP,
					},
					Data: respRaw,
				}
				return rtr.SendEtherTalkDDP(srcHWAddr, outDDP)
			}

			// Inside AppleTalk SE, pp 8-11:
			//
			// "Replies (but not Extended Replies) can contain any number of
			// zones lists, as long as the zones list for each network is
			// entirely contained in the Reply packet."
			//
			// and
			//
			// "The zones list for a given network must be contiguous in the
			// packet, with each zone name in that list preceded by the first
			// network number in the range of the requested network."
			size := 2
			for _, zl := range networks {
				for _, z := range zl {
					size += 3 + len(z) // Network number, length byte, string
				}
			}

			if size <= atalk.DDPMaxDataSize {
				// Send one non-extended reply packet with all the data
				log.Printf("ZIP: Replying with non-extended Reply: %v", networks)
				return sendReply(&zip.ReplyPacket{
					Extended: false,
					// "Replies contain the number of zones lists indicated in
					// the Reply header."
					NetworkCount: uint8(len(networks)),
					Networks:     networks,
				})
			}

			// Send Extended Reply packets, 1 or more for each network
			//
			// "Extended Replies can contain only one zones list."
			for nn, zl := range networks {
				rem := zl // rem: remaining zone names to send for this network
				for len(rem) > 0 {
					size := 2
					var chunk []string // chunk: zone names to send now
					for _, z := range rem {
						size += 3 + len(z)
						if size > atalk.DDPMaxDataSize {
							break
						}
						chunk = append(chunk, z)
					}
					rem = rem[len(chunk):]

					nets := map[ddp.Network][]string{
						nn: chunk,
					}
					log.Printf("ZIP: Replying with Extended Reply: %v", nets)
					err := sendReply(&zip.ReplyPacket{
						Extended: true,
						// "The network count in the header indicates, not the
						// number of zones names in the packet, but the number
						// of zone names in the entire zones list for the
						// requested network, which may span more than one
						// packet."
						NetworkCount: uint8(len(zl)),
						Networks:     nets,
					})
					if err != nil {
						return err
					}
				}
			}
			return nil

		case *zip.GetNetInfoPacket:
			log.Printf("ZIP: Got GetNetInfo for zone %q", zipkt.ZoneName)

			// Only running a network with one zone for now.
			resp := &zip.GetNetInfoReplyPacket{
				ZoneInvalid:     zipkt.ZoneName != rtr.Config.EtherTalk.ZoneName,
				UseBroadcast:    false,
				OnlyOneZone:     true,
				NetStart:        rtr.Config.EtherTalk.NetStart,
				NetEnd:          rtr.Config.EtherTalk.NetEnd,
				ZoneName:        zipkt.ZoneName, // has to match request
				MulticastAddr:   atalk.MulticastAddr(rtr.Config.EtherTalk.ZoneName),
				DefaultZoneName: rtr.Config.EtherTalk.ZoneName,
			}
			log.Printf("ZIP: Replying with GetNetInfo-Reply: %+v", resp)

			respRaw, err := resp.Marshal()
			if err != nil {
				return fmt.Errorf("couldn't marshal %T: %w", resp, err)
			}

			// "In cases where a node's provisional address is
			// invalid, routers will not be able to respond to
			// the node in a directed manner. An address is
			// invalid if the network number is neither in the
			// startup range nor in the network number range
			// assigned to the node's network. In these cases,
			// if the request was sent via a broadcast, the
			// routers should respond with a broadcast."
			outDDP := &ddp.ExtPacket{
				ExtHeader: ddp.ExtHeader{
					Size:      uint16(len(respRaw)) + atalk.DDPExtHeaderSize,
					Cksum:     0,
					DstNet:    ddpkt.SrcNet,
					DstNode:   ddpkt.SrcNode,
					DstSocket: ddpkt.SrcSocket,
					SrcNet:    rtr.MyDDPAddr.Network,
					SrcNode:   rtr.MyDDPAddr.Node,
					SrcSocket: 6,
					Proto:     ddp.ProtoZIP,
				},
				Data: respRaw,
			}
			if ddpkt.DstNet == 0x0000 {
				outDDP.DstNet = 0x0000
			}
			if ddpkt.DstNode == 0xFF {
				outDDP.DstNode = 0xFF
			}

			// If it's a broadcast packet, broadcast it. Otherwise don't
			dstEth := ethertalk.AppleTalkBroadcast
			if outDDP.DstNode != 0xFF {
				dstEth = srcHWAddr
			}

			return rtr.SendEtherTalkDDP(dstEth, outDDP)

		default:
			return fmt.Errorf("TODO: handle type %T", zipkt)
		}

	default:
		return fmt.Errorf("invalid DDP type %d on socket 6", ddpkt.Proto)
	}
}
