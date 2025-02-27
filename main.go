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
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/router"
	"drjosh.dev/jrouter/status"

	"github.com/google/gopacket/pcap"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
)

var hasPortRE = regexp.MustCompile(`:\d+$`)

var configFilePath = flag.String("config", "jrouter.yaml", "Path to configuration file to use")

func main() {
	// For some reason it occasionally panics and the panics have no traceback?
	// This didn't help:
	// debug.SetTraceback("all")
	// I think some dependency is calling recover in a defer too broadly.

	flag.Parse()
	log.Println("jrouter")

	cfg, err := router.LoadConfig(*configFilePath)
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

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		log.Fatalf("AURP: Couldn't listen on udp4:%d: %v", cfg.ListenPort, err)
	}
	defer ln.Close()
	log.Printf("AURP: Listening on %v", ln.LocalAddr())

	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM is what Docker sends the container process to let it clean up.
	// Fortunately syscall.SIGTERM is defined even when GOOS=windows.
	log.Println("Press ^C or send SIGINT or SIGTERM to stop the router gracefully")
	ctx, _ := signal.NotifyContext(cctx, os.Interrupt, syscall.SIGTERM)

	// --------------------------------- HTTP ---------------------------------
	if cfg.MonitoringAddr == "" {
		log.Print("monitoring_addr is empty - disabling the monitoring HTTP server")
	} else {
		http.HandleFunc("/status", status.Handle)
		http.Handle("/metrics", promhttp.Handler())
		go func() {
			log.Print(http.ListenAndServe(cfg.MonitoringAddr, nil))
		}()
	}

	// --------------------------------- Pcap ---------------------------------
	if len(cfg.EtherTalk) == 0 {
		log.Fatal("The ethertalk config in jrouter.yaml was empty; at least one entry is required")
	}

	var ethertalkPorts []*router.EtherTalkPort

	for _, etcfg := range cfg.EtherTalk {
		// First check the interface
		iface, err := net.InterfaceByName(etcfg.Device)
		if err != nil {
			log.Fatalf("Couldn't find interface named %q: %v", etcfg.Device, err)
		}

		myHWAddr := ethernet.Addr(iface.HardwareAddr)
		if etcfg.EthAddr != "" {
			// Override myHWAddr with the configured address
			netHWAddr, err := net.ParseMAC(etcfg.EthAddr)
			if err != nil {
				log.Fatalf("Couldn't parse ethertalk.ethernet_addr value %q: %v", etcfg.EthAddr, err)
			}
			myHWAddr = ethernet.Addr(netHWAddr)
		}

		handle, err := pcap.OpenLive(etcfg.Device, 4096, true, 100*time.Millisecond)
		if err != nil {
			log.Fatalf("Couldn't open %q for packet capture: %v", etcfg.Device, err)
		}
		bpfFilter := fmt.Sprintf("(atalk or aarp) and (ether multicast or ether dst %s)", myHWAddr)
		if err := handle.SetBPFFilter(bpfFilter); err != nil {
			handle.Close()
			log.Fatalf("Couldn't set BPF filter on packet capture: %v", err)
		}
		defer handle.Close()

		ethertalkPorts = append(ethertalkPorts, &router.EtherTalkPort{
			Device:          etcfg.Device,
			EthernetAddr:    myHWAddr,
			NetStart:        etcfg.NetStart,
			NetEnd:          etcfg.NetEnd,
			DefaultZoneName: etcfg.ZoneName,
			AvailableZones:  router.SetFromSlice([]string{etcfg.ZoneName}),
			PcapHandle:      handle,
			// Router: set below
		})
	}

	// -------------------------------- Tables --------------------------------
	routes := router.NewRouteTable()
	status.AddItem(ctx, "Routing table", routingTableTemplate, func(context.Context) (any, error) {
		rs := routes.Dump()
		slices.SortFunc(rs, func(ra, rb *router.Route) int {
			return cmp.Compare(ra.NetStart, rb.NetStart)
		})
		return rs, nil
	})

	// -------------------------------- Peers ---------------------------------
	var peersMu sync.Mutex
	peersByIP := make(map[[4]byte]*router.AURPPeer)
	status.AddItem(ctx, "AURP Peers", peerTableTemplate, func(context.Context) (any, error) {
		var peerInfo []*router.AURPPeer
		func() {
			peersMu.Lock()
			defer peersMu.Unlock()
			peerInfo = make([]*router.AURPPeer, 0, len(peersByIP))
			for _, p := range peersByIP {
				peerInfo = append(peerInfo, p)
			}
		}()
		slices.SortFunc(peerInfo, func(pa, pb *router.AURPPeer) int {
			return cmp.Or(
				-cmp.Compare(
					bool2Int(pa.ReceiverState() == router.ReceiverConnected),
					bool2Int(pb.ReceiverState() == router.ReceiverConnected),
				),
				-cmp.Compare(
					bool2Int(pa.SenderState() == router.SenderConnected),
					bool2Int(pb.SenderState() == router.SenderConnected),
				),
				cmp.Compare(pa.ConfiguredAddr, pb.ConfiguredAddr),
			)
		})
		return peerInfo, nil
	})

	var nextConnID uint16
	for nextConnID == 0 {
		nextConnID = uint16(rand.IntN(0x10000))
	}

	var wg sync.WaitGroup
	goPeerHandler := func(p *router.AURPPeer) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Handle(ctx)
		}()
	}

	// ------------------------- Configured peer setup ------------------------
	if cfg.PeerListURL != "" {
		log.Printf("Fetching peer list from %s...", cfg.PeerListURL)
		existing := len(cfg.Peers)
		func() {
			resp, err := http.Get(cfg.PeerListURL)
			if err != nil {
				log.Fatalf("Couldn't fetch peer list: %v", err)
			}
			defer resp.Body.Close()

			sc := bufio.NewScanner(resp.Body)
			for sc.Scan() {
				p := strings.TrimSpace(sc.Text())
				if p == "" {
					continue
				}
				cfg.Peers = append(cfg.Peers, p)
			}
			if err := sc.Err(); err != nil {
				log.Fatalf("Couldn't scan peer list response: %v", err)
			}
		}()
		log.Printf("Fetched list containing %d peers", len(cfg.Peers)-existing)
	}

	for _, peerStr := range cfg.Peers {
		if !hasPortRE.MatchString(peerStr) {
			peerStr += ":387"
		}

		raddr, err := net.ResolveUDPAddr("udp4", peerStr)
		if err != nil {
			log.Printf("couldn't resolve UDP address, skipping: %v", err)
			continue
		}
		log.Printf("resolved %q to %v", peerStr, raddr)

		if raddr.IP.Equal(localIP) {
			log.Printf("%v == %v == me, skipping", peerStr, raddr)
			continue
		}

		peer := router.NewAURPPeer(routes, ln, peerStr, raddr.IP, localDI, nil, nextConnID)
		aurp.Inc(&nextConnID)
		peersMu.Lock()
		peersByIP[[4]byte(raddr.IP)] = peer
		peersMu.Unlock()
		goPeerHandler(peer)
	}

	// -------------------------------- Router --------------------------------
	rooter := &router.Router{
		Config:     cfg,
		RouteTable: routes,
		// ZoneTable:  zones,
	}

	// Attach ports to router
	rooter.Ports = append(rooter.Ports, ethertalkPorts...)
	for _, etPort := range ethertalkPorts {
		// Attach router to ports
		etPort.Router = rooter

		// Add port to routing table
		route, err := routes.UpsertRoute(etPort, true /* extended */, etPort.NetStart, etPort.NetEnd, 0)
		if err != nil {
			log.Fatalf("Couldn't create route for EtherTalk port: %v", err)
		}
		route.ZoneNames = etPort.AvailableZones

		// Run AARP and RTMP on each port.
		etPort.AARPMachine = router.NewAARPMachine(etPort, etPort.EthernetAddr)
		go etPort.AARPMachine.Run(ctx)
		go etPort.RunRTMP(ctx)

		// Finally, start handling packets.
		wg.Add(1)
		go func() {
			defer wg.Done()

			ctx, setStatus, _ := status.AddSimpleItem(ctx, fmt.Sprintf("EtherTalk inbound on %s", etPort.Device))
			defer setStatus("EtherTalk Serve goroutine exited!")

			setStatus(fmt.Sprintf("Listening on %s", etPort.Device))

			etPort.Serve(ctx)
		}()
	}

	// ----------------------------- AURP inbound -----------------------------
	wg.Add(1)
	go func() {
		defer wg.Done()

		ctx, setStatus, done := status.AddSimpleItem(ctx, "AURP inbound")
		defer done()
		setStatus(fmt.Sprintf("Listening on UDP port %d", cfg.ListenPort))

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

			promLabels := prometheus.Labels{"peer": raddr.IP.String()}
			aurpPacketsInCounter.With(promLabels).Inc()
			aurpBytesInCounter.With(promLabels).Add(float64(pktlen))

			// log.Printf("AURP: Received packet of length %d from %v", pktlen, raddr)

			dh, pkt, parseErr := aurp.ParsePacket(pktbuf[:pktlen])
			if parseErr != nil {
				log.Printf("AURP: Failed to parse packet: %v", parseErr)
				aurpInvalidPacketsInCounter.With(promLabels).Inc()
				continue
			}
			if readErr != nil {
				log.Printf("AURP: Failed to read packet: %v", readErr)
				return
			}

			log.Printf("AURP: Got %T from peer %v (domain identifier %v)", pkt, raddr, dh.SourceDI)

			// Existing peer?
			ra := [4]byte(raddr.IP)
			peersMu.Lock()
			pr := peersByIP[ra]
			if pr == nil {
				if !cfg.OpenPeering {
					log.Printf("AURP: Got packet from %v, but it's not in my config and open peering is disabled; dropping the packet", raddr)
					peersMu.Unlock()
					continue
				}
				// New peer!
				pr = router.NewAURPPeer(routes, ln, "", raddr.IP, localDI, dh.SourceDI, nextConnID)
				aurp.Inc(&nextConnID)
				peersByIP[ra] = pr
				goPeerHandler(pr)
			}
			peersMu.Unlock()

			switch dh.PacketType {
			case aurp.PacketTypeRouting:
				// It's AURP routing data.
				// Pass the packet to the goroutine in charge of this peer.
				select {
				case pr.ReceiveCh <- pkt:
					// That's it for us.

				case <-ctx.Done():
					return
				}
				continue

			case aurp.PacketTypeAppleTalk:
				apkt, ok := pkt.(*aurp.AppleTalkPacket)
				if !ok {
					log.Printf("AURP: Got %T but domain header packet type was %v ?", pkt, dh.PacketType)
					continue
				}

				// Route or otherwise handle the encapsulated AppleTalk traffic
				ddpkt := new(ddp.ExtPacket)
				if err := ddp.ExtUnmarshal(apkt.Data, ddpkt); err != nil {
					log.Printf("AURP: Couldn't unmarshal encapsulated DDP packet: %v", err)
					continue
				}
				// log.Printf("DDP/AURP: Got %d.%d.%d -> %d.%d.%d proto %d data len %d",
				// 	ddpkt.SrcNet, ddpkt.SrcNode, ddpkt.SrcSocket,
				// 	ddpkt.DstNet, ddpkt.DstNode, ddpkt.DstSocket,
				// 	ddpkt.Proto, len(ddpkt.Data))

				// Is it addressed to me?
				var localPort *router.EtherTalkPort
				for _, port := range rooter.Ports {
					if ddpkt.DstNet >= port.NetStart && ddpkt.DstNet <= port.NetEnd {
						localPort = port
						break
					}
				}
				if ddpkt.DstNode == 0 && localPort != nil { // Node 0 = any router for the network = me
					// Is it NBP? FwdReq needs translating.
					if ddpkt.DstSocket != 2 {
						// Something else?? TODO
						log.Printf("DDP/AURP: I don't have anything 'listening' on socket %d", ddpkt.DstSocket)
						continue
					}
					// It's NBP, specifically it should be a FwdReq
					if err := rooter.HandleNBPFromAURP(ctx, ddpkt); err != nil {
						log.Printf("NBP/DDP/AURP: %v", err)
					}
					continue
				}

				// Route the packet!
				if err := rooter.Forward(ctx, ddpkt); err != nil {
					log.Printf("DDP/AURP: Couldn't route packet: %v", err)
				}

			default:
				log.Printf("AURP: Got unknown packet type %v", dh.PacketType)
			}
		}
	}()

	// -------------------------------- Close ---------------------------------
	wg.Wait()
}

func bool2Int(b bool) int {
	if b {
		return 1
	}
	return 0
}
