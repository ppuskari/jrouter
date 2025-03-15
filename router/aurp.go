/*
   Copyright 2025 Josh Deprez

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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"sync"
	"time"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/status"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sfiera/multitalk/pkg/ddp"
)

// AURPInput is a packet listening loop on a UDP connection for AURP.
func (r *Router) AURPInput(ctx context.Context, logger *slog.Logger, wg *sync.WaitGroup, cfg *Config, udpConn *net.UDPConn, localDI aurp.DomainIdentifier) {
	defer wg.Done()

	ctx, setStatus, _ := status.AddSimpleItem(ctx, "AURP inbound")
	defer setStatus("Not running!")
	setStatus(fmt.Sprintf("Listening on UDP port %d", cfg.ListenPort))

	for {
		if ctx.Err() != nil {
			return
		}
		udpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		pktbuf := make([]byte, 4096)
		pktlen, raddr, readErr := udpConn.ReadFromUDP(pktbuf)

		var operr *net.OpError
		if errors.As(readErr, &operr) && operr.Timeout() {
			continue
		}

		promLabels := prometheus.Labels{"peer": raddr.IP.String()}
		aurpPacketsInCounter.With(promLabels).Inc()
		aurpBytesInCounter.With(promLabels).Add(float64(pktlen))

		// logger.Debug("AURP: Received packet", "pktlen", pktlen, "raddr", raddr)

		dh, pkt, parseErr := aurp.ParsePacket(pktbuf[:pktlen])
		if parseErr != nil {
			logger.Warn("AURP: Failed to parse packet", "error", parseErr, "pktlen", pktlen, "raddr", raddr)
			aurpInvalidPacketsInCounter.With(promLabels).Inc()
			continue
		}
		if readErr != nil {
			logger.Warn("AURP: Failed to read packet", "error", readErr, "pktlen", pktlen, "raddr", raddr)
			return
		}

		logger.Debug("AURP: Read packet from peer", "pkt-type", reflect.TypeOf(pkt), "raddr", raddr, "sourceDI", dh.SourceDI)

		var peer *AURPPeer
		if cfg.OpenPeering {
			p, err := r.AURPPeers.LookupOrCreate(ctx, logger, wg, r.RouteTable, udpConn, "", raddr.IP, localDI, dh.SourceDI)
			if err != nil {
				logger.Warn("AURP: peer LookupOrCreate", "error", err)
				continue
			}
			peer = p
		} else {
			p, err := r.AURPPeers.Lookup(raddr.IP)
			if err != nil {
				logger.Error("AURP: peer Lookup", "error", err)
				continue
			}
			if p == nil {
				logger.Warn("AURP: Got packet from peer not in config and open peering is disabled; dropping the packet", "raddr", raddr)
				continue
			}
			peer = p
		}

		switch dh.PacketType {
		case aurp.PacketTypeRouting:
			// It's AURP routing data.
			// Pass the packet to the goroutine in charge of this peer.
			select {
			case peer.ReceiveCh <- pkt:
				// That's it for us.

			case <-ctx.Done():
				return
			}
			continue

		case aurp.PacketTypeAppleTalk:
			apkt, ok := pkt.(*aurp.AppleTalkPacket)
			if !ok {
				logger.Error("AURP: Packet and domain header type conflict", "pkt-type", reflect.TypeOf(pkt), "dh-packettype", dh.PacketType)
				continue
			}

			// Route or otherwise handle the encapsulated AppleTalk traffic
			ddpkt := new(ddp.ExtPacket)
			if err := ddp.ExtUnmarshal(apkt.Data, ddpkt); err != nil {
				logger.Error("AURP: Couldn't unmarshal encapsulated DDP packet", "error", err)
				continue
			}
			// logger.Debug(fmt.Sprintf("DDP/AURP: Got %d.%d.%d -> %d.%d.%d proto %d data len %d",
			// 	ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
			// 	ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
			// 	ddpkt.Proto, len(ddpkt.Data)))

			// Is it addressed to me?
			var localPort *EtherTalkPort
			for _, port := range r.Ports {
				if ddpkt.DstNet >= port.NetStart && ddpkt.DstNet <= port.NetEnd {
					localPort = port
					break
				}
			}
			if ddpkt.DstNode == 0 && localPort != nil { // Node 0 = any router for the network = me
				// Is it NBP? FwdReq needs translating.
				if ddpkt.DstSocket != 2 {
					// Something else?? TODO
					logger.Debug("DDP/AURP: I don't have anything 'listening' on that socket", "dst-socket", ddpkt.DstSocket)
					continue
				}
				// It's NBP, specifically it should be a FwdReq
				if err := r.HandleNBPFromAURP(ctx, ddpkt); err != nil {
					logger.Error("NBP/DDP/AURP handling", "error", err)
				}
				continue
			}

			// Output the packet!
			// Note that AIR does not increment the hop count for packets
			// flowing from AURP->local, probably because the hop count was
			// incremented when it was sent by the remote peer. (To put it
			// another way, the whole network of AURP nodes acts as one huge
			// "router".) Hence rooter.Output and not rooter.Forward.
			if err := r.Output(ctx, ddpkt); err != nil {
				logger.Error("DDP/AURP: Couldn't route packet", "error", err)
			}

		default:
			logger.Error("AURP: Unknown packet type", "dh-packettype", dh.PacketType)
		}
	}
}
