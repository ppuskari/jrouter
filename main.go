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
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"regexp"
	"sync"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
	"github.com/google/gopacket/pcap"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

var hasPortRE = regexp.MustCompile(`:\d+$`)

var configFilePath = flag.String("config", "jrouter.yaml", "Path to configuration file to use")

func main() {
	flag.Parse()
	log.Println("jrouter")

	cfg, err := loadConfig(*configFilePath)
	if err != nil {
		log.Fatalf("Couldn't load configuration file: %v", err)
	}

	localIP := net.ParseIP(cfg.LocalIP).To4()
	if localIP == nil {
		iaddrs, err := net.InterfaceAddrs()
		if err != nil {
			log.Fatalf("Couldn't read network interface addresses: %v", err)
		}
		for _, iaddr := range iaddrs {
			inet, ok := iaddr.(*net.IPNet)
			if !ok {
				continue
			}
			if !inet.IP.IsGlobalUnicast() {
				continue
			}
			localIP = inet.IP.To4()
			if localIP != nil {
				break
			}
		}
		if localIP == nil {
			log.Fatalf("No global unicast IPv4 addresses on any network interfaces, and no valid local_ip address in configuration")
		}
	}
	localDI := aurp.IPDomainIdentifier(localIP)

	log.Printf("Using %v as local domain identifier", localIP)

	log.Printf("EtherTalk configuration: %+v", cfg.EtherTalk)

	peers := make(map[udpAddr]*peer)
	var nextConnID uint16
	for nextConnID == 0 {
		nextConnID = uint16(rand.IntN(0x10000))
	}

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}
	log.Printf("Listening on %v", ln.LocalAddr())

	log.Println("Press ^C or send SIGINT to stop the router gracefully")
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ := signal.NotifyContext(cctx, os.Interrupt)

	// Open PCAP session
	// First check the interface
	iface, err := net.InterfaceByName(cfg.EtherTalk.Device)
	if err != nil {
		log.Fatalf("Couldn't find interface named %q: %v", cfg.EtherTalk.Device, err)
	}
	myHWAddr := ethernet.Addr(iface.HardwareAddr)
	if cfg.EtherTalk.EthAddr != "" {
		// Override myHWAddr with the configured address
		netHWAddr, err := net.ParseMAC(cfg.EtherTalk.EthAddr)
		if err != nil {
			log.Fatalf("Couldn't parse ethertalk.ethernet_addr value %q: %v", cfg.EtherTalk.EthAddr, err)
		}
		myHWAddr = ethernet.Addr(netHWAddr)
	}

	pcapHandle, err := pcap.OpenLive(cfg.EtherTalk.Device, 4096, true, 100*time.Millisecond)
	if err != nil {
		log.Fatalf("Couldn't open %q for packet capture: %v", cfg.EtherTalk.Device, err)
	}
	bpfFilter := fmt.Sprintf("(atalk or aarp) and (ether multicast or ether dst %s)", myHWAddr)
	if err := pcapHandle.SetBPFFilter(bpfFilter); err != nil {
		pcapHandle.Close()
		log.Fatalf("Couldn't set BPF filter on packet capture: %v", err)
	}
	defer pcapHandle.Close()

	// Wait until all peer handlers have finished before closing the port
	var handlersWG sync.WaitGroup
	defer func() {
		log.Print("Waiting for handlers to return...")
		handlersWG.Wait()
		ln.Close()
	}()
	goPeerHandler := func(p *peer) {
		handlersWG.Add(1)
		go func() {
			defer handlersWG.Done()
			p.handle(ctx)
		}()
	}

	// -------------------------------- Tables --------------------------------
	routing := NewRoutingTable()
	zones := NewZoneTable()
	zones.Upsert(cfg.EtherTalk.NetStart, cfg.EtherTalk.ZoneName, true)

	// ------------------------- Configured peer setup ------------------------
	for _, peerStr := range cfg.Peers {
		if !hasPortRE.MatchString(peerStr) {
			peerStr += ":387"
		}

		raddr, err := net.ResolveUDPAddr("udp4", peerStr)
		if err != nil {
			log.Fatalf("Invalid UDP address: %v", err)
		}
		log.Printf("resolved %q to %v", peerStr, raddr)

		peer := &peer{
			cfg: cfg,
			tr: &aurp.Transport{
				LocalDI:     localDI,
				RemoteDI:    aurp.IPDomainIdentifier(raddr.IP),
				LocalConnID: nextConnID,
			},
			conn:         ln,
			raddr:        raddr,
			recv:         make(chan aurp.Packet, 1024),
			routingTable: routing,
			zoneTable:    zones,
		}
		aurp.Inc(&nextConnID)
		peers[udpAddrFromNet(raddr)] = peer
		goPeerHandler(peer)
	}

	// --------------------------------- AARP ---------------------------------
	aarpMachine := NewAARPMachine(cfg, pcapHandle, myHWAddr)
	aarpCh := make(chan *ethertalk.Packet, 1024)
	go aarpMachine.Run(ctx, aarpCh)

	// --------------------------------- RTMP ---------------------------------
	rtmpMachine := &RTMPMachine{
		aarp:         aarpMachine,
		cfg:          cfg,
		pcapHandle:   pcapHandle,
		routingTable: routing,
	}
	rtmpCh := make(chan *ddp.ExtPacket, 1024)
	go rtmpMachine.Run(ctx, rtmpCh)

	// ---------------------- Raw AppleTalk/AARP inbound ----------------------
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			rawPkt, _, err := pcapHandle.ReadPacketData()
			if errors.Is(err, pcap.NextErrorTimeoutExpired) {
				continue
			}
			if errors.Is(err, io.EOF) || errors.Is(err, pcap.NextErrorNoMorePackets) {
				return
			}
			if err != nil {
				log.Printf("Couldn't read AppleTalk / AARP packet data: %v", err)
				return
			}

			ethFrame := new(ethertalk.Packet)
			if err := ethertalk.Unmarshal(rawPkt, ethFrame); err != nil {
				log.Printf("Couldn't unmarshal EtherTalk frame: %v", err)
				continue
			}

			// Ignore if sent by me
			if ethFrame.Src == myHWAddr {
				continue
			}

			// TODO: filter ethFrame.Dst to myHWAddr, the broadcast address,
			// or the relevant zone multicast address

			switch ethFrame.SNAPProto {
			case ethertalk.AARPProto:
				// log.Print("Got an AARP frame")
				aarpCh <- ethFrame

			case ethertalk.AppleTalkProto:
				// log.Print("Got an AppleTalk frame")
				ddpkt := new(ddp.ExtPacket)
				if err := ddp.ExtUnmarshal(ethFrame.Payload, ddpkt); err != nil {
					log.Printf("Couldn't unmarshal DDP packet: %v", err)
					continue
				}
				log.Printf("DDP: src (%d.%d s %d) dst (%d.%d s %d) proto %d data len %d",
					ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
					ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
					ddpkt.Proto, len(ddpkt.Data))

				// Glean address info for AMT
				srcAddr := ddp.Addr{Network: ddpkt.SrcNet, Node: ddpkt.SrcNode}
				aarpMachine.Learn(srcAddr, ethFrame.Src)
				// log.Printf("DDP: Gleaned that %d.%d -> %v", srcAddr.Network, srcAddr.Node, ethFrame.Src)

				// Packet for us? First, who am I?
				myAddr, ok := aarpMachine.Address()
				if !ok {
					continue
				}

				// Our network?
				// "The network number 0 is reserved to mean unknown; by default
				// it specifies the local network to which the node is
				// connected. Packets whose destination network number is 0 are
				// addressed to a node on the local network."
				if ddpkt.DstNet != 0 && (ddpkt.DstNet < cfg.EtherTalk.NetStart || ddpkt.DstNet > cfg.EtherTalk.NetEnd) {
					// Is it for a network in the routing table?
					rt := routing.LookupRoute(ddpkt.DstNet)
					if rt == nil {
						log.Printf("DDP: no route for network %d", ddpkt.DstNet)
						continue
					}

					// Encap ethPacket.Payload into an AURP packet
					log.Printf("DDP: forwarding to AURP peer %v", rt.Peer.tr.RemoteDI)
					if _, err := rt.Peer.send(rt.Peer.tr.NewAppleTalkPacket(ethFrame.Payload)); err != nil {
						log.Printf("DDP: Couldn't forward packet to AURP peer: %v", err)
					}

					continue
				}

				// To me?
				// "Node ID 0 indicates any router on the network"- I'm a router
				// "node ID $FF indicates either a network-wide or zone-specific
				// broadcast"- that's relevant
				if ddpkt.DstNode != 0 && ddpkt.DstNode != 0xff && ddpkt.DstNode != myAddr.Proto.Node {
					continue
				}

				switch ddpkt.DstSocket {
				case 1: // The RTMP socket
					rtmpCh <- ddpkt

				case 2: // The NIS (name information socket / NBP socket)
					if err := handleNBP(pcapHandle, myHWAddr, ethFrame.Src, myAddr, zones, routing, cfg, ddpkt); err != nil {
						log.Printf("NBP: Couldn't handle: %v", err)
					}

				case 4: // The AEP socket
					if err := handleAEP(pcapHandle, myHWAddr, ethFrame.Src, ddpkt); err != nil {
						log.Printf("AEP: Couldn't handle: %v", err)
					}

				case 6: // The ZIS (zone information socket / ZIP socket)
					if err := handleZIP(pcapHandle, ethFrame.Src, myHWAddr, myAddr, cfg, zones, ddpkt); err != nil {
						log.Printf("ZIP: couldn't handle: %v", err)
					}

				default:
					log.Printf("DDP: No handler for socket %d", ddpkt.DstSocket)
				}

			default:
				log.Printf("Read unknown packet %s -> %s with payload %x", ethFrame.Src, ethFrame.Dst, ethFrame.Payload)

			}
		}
	}()

	// ----------------------------- AURP inbound -----------------------------
	for {
		if ctx.Err() != nil {
			return
		}
		ln.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		pktbuf := make([]byte, 4096)
		pktlen, raddr, readErr := ln.ReadFromUDP(pktbuf)

		var operr *net.OpError
		if errors.As(readErr, &operr) && operr.Timeout() {
			continue
		}

		// log.Printf("AURP: Received packet of length %d from %v", pktlen, raddr)

		dh, pkt, parseErr := aurp.ParsePacket(pktbuf[:pktlen])
		if parseErr != nil {
			log.Printf("AURP: Failed to parse packet: %v", parseErr)
			continue
		}
		if readErr != nil {
			log.Printf("AURP: Failed to read packet: %v", readErr)
			return
		}

		if apkt, ok := pkt.(*aurp.AppleTalkPacket); ok {
			ddpkt := new(ddp.ExtPacket)
			if err := ddp.ExtUnmarshal(apkt.Data, ddpkt); err != nil {
				log.Printf("AURP: Couldn't unmarshal encapsulated DDP packet: %v", err)
				continue
			}
			log.Printf("DDP/AURP: Got %d.%d.%d -> %d.%d.%d proto %d data len %d",
				ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
				ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
				ddpkt.Proto, len(ddpkt.Data))

			// "Route" the packet
			// Since for now there's only one local network, the routing
			// decision is pretty easy
			// TODO: Fix this to support other AppleTalk routers
			if ddpkt.DstNet < cfg.EtherTalk.NetStart || ddpkt.DstNet > cfg.EtherTalk.NetEnd {
				log.Print("DDP/AURP: dropping packet not addressed to our EtherTalk range")
				continue
			}

			// Check and adjust the Hop Count
			// Note the ddp package doesn't make this simple
			hopCount := (ddpkt.Size & 0x3C00) >> 10
			if hopCount >= 15 {
				log.Printf("DDP/AURP: hop count exceeded (%d >= 15)", hopCount)
				continue
			}
			hopCount++
			ddpkt.Size &^= 0x3C00
			ddpkt.Size |= hopCount << 10

			// Is it addressed to me? Is it NBP?
			if ddpkt.DstNode == 0 { // Node 0 = the router for the network
				if ddpkt.DstSocket != 2 {
					// Something else?? TODO
					log.Printf("DDP/AURP: I don't have anything 'listening' on socket %d", ddpkt.DstSocket)
					continue
				}
				// It's NBP
				if err := handleNBPInAURP(pcapHandle, myHWAddr, ddpkt); err != nil {
					log.Printf("NBP/DDP/AURP: %v", err)
				}
				continue
			}

			// Note: resolving AARP can block
			dstEth, err := aarpMachine.Resolve(ctx, ddp.Addr{Network: ddpkt.DstNet, Node: ddpkt.DstNode})
			if err != nil {
				log.Printf("DDP/AURP: couldn't resolve DDP dest %d.%d to an Ethernet address", ddpkt.DstNet, ddpkt.DstNode)
				continue
			}

			outFrame, err := ethertalk.AppleTalk(myHWAddr, *ddpkt)
			if err != nil {
				log.Printf("DDP/AURP: couldn't create output frame: %v", err)
				continue
			}
			outFrame.Dst = dstEth

			outFrameRaw, err := ethertalk.Marshal(*outFrame)
			if err != nil {
				log.Printf("DDP/AURP: couldn't marshal output frame: %v", err)
				continue
			}
			if err := pcapHandle.WritePacketData(outFrameRaw); err != nil {
				log.Printf("DDP/AURP: couldn't write output frame to device: %v", err)
			}
			continue
		}

		log.Printf("AURP: Got %T", pkt)

		// Existing peer?
		ra := udpAddrFromNet(raddr)
		pr := peers[ra]
		if pr == nil {
			// New peer!
			pr = &peer{
				cfg: cfg,
				tr: &aurp.Transport{
					LocalDI:     localDI,
					RemoteDI:    dh.SourceDI, // platinum rule
					LocalConnID: nextConnID,
				},
				conn:         ln,
				raddr:        raddr,
				recv:         make(chan aurp.Packet, 1024),
				routingTable: routing,
				zoneTable:    zones,
			}
			aurp.Inc(&nextConnID)
			peers[ra] = pr
			goPeerHandler(pr)
		}

		// Pass the packet to the goroutine in charge of this peer.
		select {
		case pr.recv <- pkt:
			// That's it for us.

		case <-ctx.Done():
			return
		}
	}
}

// Hashable net.UDPAddr
type udpAddr struct {
	ipv4 [4]byte
	port uint16
}

func udpAddrFromNet(a *net.UDPAddr) udpAddr {
	return udpAddr{
		ipv4: [4]byte(a.IP.To4()),
		port: uint16(a.Port),
	}
}

func (u udpAddr) toNet() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   u.ipv4[:],
		Port: int(u.port),
	}
}
