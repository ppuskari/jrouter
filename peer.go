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
	"bytes"
	"context"
	"log"
	"net"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

const (
	// TODO: check these parameters
	lastHeardFromTimer = 10 * time.Second
	tickleRetryLimit   = 10
	sendRetryTimer     = 10 * time.Second
	sendRetryLimit     = 5
)

type receiverState int

const (
	rsUnconnected receiverState = iota
	rsConnected
	rsWaitForOpenRsp
	rsWaitForRIRsp
	rsWaitForTickleAck
)

func (rs receiverState) String() string {
	return map[receiverState]string{
		rsUnconnected:      "unconnected",
		rsConnected:        "connected",
		rsWaitForOpenRsp:   "waiting for Open-Rsp",
		rsWaitForRIRsp:     "waiting for RI-Rsp",
		rsWaitForTickleAck: "waiting for Tickle-Ack",
	}[rs]
}

type senderState int

const (
	ssUnconnected senderState = iota
	ssConnected
	ssWaitForRIAck1
	ssWaitForRIAck2
	ssWaitForRIAck3
)

func (ss senderState) String() string {
	return map[senderState]string{
		ssUnconnected:   "unconnected",
		ssConnected:     "connected",
		ssWaitForRIAck1: "waiting for RI-Ack (1)",
		ssWaitForRIAck2: "waiting for RI-Ack (2)",
		ssWaitForRIAck3: "waiting for RI-Ack (3)",
	}[ss]
}

type peer struct {
	cfg   *config
	tr    *aurp.Transport
	conn  *net.UDPConn
	raddr *net.UDPAddr
	recv  chan aurp.Packet
}

// send encodes and sends pkt to the remote host.
func (p *peer) send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	log.Printf("Sending %T (len %d) to %v", pkt, b.Len(), p.raddr)
	return p.conn.WriteToUDP(b.Bytes(), p.raddr)
}

func (p *peer) handle(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastHeardFrom := time.Now()
	lastSend := time.Now()
	sendRetries := 0

	rstate := rsUnconnected
	sstate := ssUnconnected

	// Write an Open-Req packet
	if _, err := p.send(p.tr.NewOpenReqPacket(nil)); err != nil {
		log.Printf("Couldn't send Open-Req packet: %v", err)
		return err
	}

	rstate = rsWaitForOpenRsp

	for {
		select {
		case <-ctx.Done():
			if sstate == ssUnconnected {
				// Return immediately
				return ctx.Err()
			}
			// Send a best-effort Router Down before returning
			if _, err := p.send(p.tr.NewRDPacket(aurp.ErrCodeNormalClose)); err != nil {
				log.Printf("Couldn't send RD packet: %v", err)
			}
			return ctx.Err()

		case <-ticker.C:
			switch rstate {
			case rsWaitForOpenRsp:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= sendRetryLimit {
					log.Printf("Send retry limit reached while waiting for Open-Rsp, closing connection")
					rstate = rsUnconnected
					break
				}

				// Send another Open-Req
				sendRetries++
				lastSend = time.Now()
				if _, err := p.send(p.tr.NewOpenReqPacket(nil)); err != nil {
					log.Printf("Couldn't send Open-Req packet: %v", err)
					return err
				}

			case rsConnected:
				// Check LHFT, send tickle?
				if time.Since(lastHeardFrom) <= lastHeardFromTimer {
					break
				}
				if _, err := p.send(p.tr.NewTicklePacket()); err != nil {
					log.Printf("Couldn't send Tickle: %v", err)
					return err
				}
				rstate = rsWaitForTickleAck
				sendRetries = 0
				lastSend = time.Now()

			case rsWaitForTickleAck:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= tickleRetryLimit {
					log.Printf("Send retry limit reached while waiting for Tickle-Ack, closing connection")
					rstate = rsUnconnected
					break
				}

				sendRetries++
				lastSend = time.Now()
				if _, err := p.send(p.tr.NewTicklePacket()); err != nil {
					log.Printf("Couldn't send Tickle: %v", err)
					return err
				}
			}

		case pkt := <-p.recv:
			lastHeardFrom = time.Now()

			switch pkt := pkt.(type) {
			case *aurp.OpenReqPacket:
				if sstate != ssUnconnected {
					log.Printf("Open-Req received but sender state is not unconnected (was %v)", sstate)
				}

				// The peer tells us their connection ID in Open-Req.
				p.tr.RemoteConnID = pkt.ConnectionID

				// Formulate a response.
				var orsp *aurp.OpenRspPacket
				switch {
				case pkt.Version != 1:
					// Respond with Open-Rsp with unknown version error.
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeInvalidVersion), nil)

				case len(pkt.Options) > 0:
					// Options? OPTIONS? We don't accept no stinkin' _options_
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeOptionNegotiation), nil)

				default:
					// Accept it I guess.
					orsp = p.tr.NewOpenRspPacket(0, 1, nil)
				}

				if _, err := p.send(orsp); err != nil {
					log.Printf("Couldn't send Open-Rsp: %v", err)
					return err
				}
				if orsp.RateOrErrCode >= 0 {
					sstate = ssConnected
				}

				// If receiver is unconnected, commence connecting
				if rstate == rsUnconnected {
					lastSend = time.Now()
					sendRetries = 0
					if _, err := p.send(p.tr.NewOpenReqPacket(nil)); err != nil {
						log.Printf("Couldn't send Open-Req packet: %v", err)
						return err
					}
					rstate = rsWaitForOpenRsp
				}

			case *aurp.OpenRspPacket:
				if rstate != rsWaitForOpenRsp {
					log.Printf("Received Open-Rsp but was not waiting for one (receiver state was %v)", rstate)
				}
				if pkt.RateOrErrCode < 0 {
					// It's an error code.
					log.Printf("Open-Rsp error code from peer %v: %d", p.raddr.IP, pkt.RateOrErrCode)
					rstate = rsUnconnected
					break
				}
				log.Printf("Data receiver is connected!")
				rstate = rsConnected

				// Send an RI-Req
				if _, err := p.send(p.tr.NewRIReqPacket()); err != nil {
					log.Printf("Couldn't send RI-Req packet: %v", err)
					return err
				}
				rstate = rsWaitForRIRsp

			case *aurp.RIReqPacket:
				if sstate != ssConnected {
					log.Printf("Received RI-Req but was not expecting one (sender state was %v)", sstate)
				}

				nets := aurp.NetworkTuples{
					{
						RangeStart: p.cfg.EtherTalk.NetStart,
						RangeEnd:   p.cfg.EtherTalk.NetEnd,
						Distance:   0,
					},
				}
				p.tr.LocalSeq = 1
				if _, err := p.send(p.tr.NewRIRspPacket(aurp.RoutingFlagLast, nets)); err != nil {
					log.Printf("Couldn't send RI-Rsp packet: %v", err)
					return err
				}
				sstate = ssWaitForRIAck1

			case *aurp.RIRspPacket:
				if rstate != rsWaitForRIRsp {
					log.Printf("Received RI-Rsp but was not waiting for one (receiver state was %v)", rstate)
				}

				log.Printf("Learned about these networks: %v", pkt.Networks)

				// TODO: Integrate info into route table

				// TODO: track which networks we don't have zone info for, and
				// only set SZI for those ?
				if _, err := p.send(p.tr.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, aurp.RoutingFlagSendZoneInfo)); err != nil {
					log.Printf("Couldn't send RI-Ack packet: %v", err)
					return err
				}
				if pkt.Flags&aurp.RoutingFlagLast != 0 {
					// No longer waiting for an RI-Rsp
					rstate = rsConnected
				}

			case *aurp.RIAckPacket:
				switch sstate {
				case ssWaitForRIAck1:
					// We sent an RI-Rsp, this is the RI-Ack we expected.

				case ssWaitForRIAck2:
					// We sent an RI-Upd, this is the RI-Ack we expected.

				case ssWaitForRIAck3:
					// We sent an RD... Why are we here?
					continue

				default:
					log.Printf("Received RI-Ack but was not waiting for one (sender state was %v)", sstate)
				}

				sstate = ssConnected

				// If SZI flag is set, send ZI-Rsp (transaction)
				// TODO: only respond with zones for networks that were in the
				// RI-Rsp that corresponded to this RI-Ack
				if pkt.Flags&aurp.RoutingFlagSendZoneInfo != 0 {
					zones := aurp.ZoneTuples{
						{
							Network: p.cfg.EtherTalk.NetStart,
							Name:    p.cfg.EtherTalk.ZoneName,
						},
					}
					if _, err := p.send(p.tr.NewZIRspPacket(zones)); err != nil {
						log.Printf("Couldn't send ZI-Rsp packet: %v", err)
					}
				}

				// TODO: Continue sending next RI-Rsp (streamed)?

			case *aurp.RIUpdPacket:
				// TODO: Integrate info into route table

			case *aurp.RDPacket:
				if rstate == rsUnconnected || rstate == rsWaitForOpenRsp {
					log.Printf("Received RD but was not expecting one (receiver state was %v)", rstate)
				}
				// TODO: Remove router from route tables

				log.Printf("Router Down: error code %d %s", pkt.ErrorCode, pkt.ErrorCode)
				// Respond with RI-Ack
				if _, err := p.send(p.tr.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
					log.Printf("Couldn't send RI-Ack: %v", err)
					return err
				}
				// Connection closed
				rstate = rsUnconnected

			case *aurp.ZIReqPacket:
				// TODO: only respond with zones for networks specified by the
				// ZI-Req
				zones := aurp.ZoneTuples{
					{
						Network: p.cfg.EtherTalk.NetStart,
						Name:    p.cfg.EtherTalk.ZoneName,
					},
				}
				if _, err := p.send(p.tr.NewZIRspPacket(zones)); err != nil {
					log.Printf("Couldn't send ZI-Rsp packet: %v", err)
					return err
				}

			case *aurp.ZIRspPacket:
				// TODO: Integrate info into zone table
				log.Printf("Learned about these zones: %v", pkt.Zones)

			case *aurp.GDZLReqPacket:
				if _, err := p.send(p.tr.NewGDZLRspPacket(-1, nil)); err != nil {
					log.Printf("Couldn't send GDZL-Rsp packet: %v", err)
					return err
				}

			case *aurp.GDZLRspPacket:
				log.Printf("Received a GDZL-Rsp, but I wouldn't have sent a GDZL-Req - that's weird")

			case *aurp.GZNReqPacket:
				if _, err := p.send(p.tr.NewGZNRspPacket(pkt.ZoneName, false, nil)); err != nil {
					log.Printf("Couldn't send GZN-Rsp packet: %v", err)
					return err
				}

			case *aurp.GZNRspPacket:
				log.Printf("Received a GZN-Rsp, but I wouldn't have sent a GZN-Req - that's weird")

			case *aurp.TicklePacket:
				// Immediately respond with Tickle-Ack
				if _, err := p.send(p.tr.NewTickleAckPacket()); err != nil {
					log.Printf("Couldn't send Tickle-Ack: %v", err)
					return err
				}

			case *aurp.TickleAckPacket:
				if rstate != rsWaitForTickleAck {
					log.Printf("Received Tickle-Ack but was not waiting for one (receiver state was %v)", rstate)
				}
				rstate = rsConnected
			}
		}
	}
}
