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
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"sync"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
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

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}
	log.Printf("Listening on %v", ln.LocalAddr())

	log.Println("Press ^C or send SIGINT to stop the router gracefully")
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, _ := signal.NotifyContext(cctx, os.Interrupt)

	// Wait until all peer handlers have finished before closing the port
	var handlersWG sync.WaitGroup
	defer func() {
		log.Print("Waiting for handlers to return...")
		handlersWG.Wait()
		ln.Close()
	}()
	goHandler := func(p *peer) {
		handlersWG.Add(1)
		go func() {
			defer handlersWG.Done()
			p.handle(ctx)
		}()
	}

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
			conn:  ln,
			raddr: raddr,
			recv:  make(chan aurp.Packet, 1024),
		}
		nextConnID++
		goHandler(peer)
		peers[udpAddrFromNet(raddr)] = peer
	}

	// Incoming packet loop
	for {
		if ctx.Err() != nil {
			return
		}
		ln.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		pktbuf := make([]byte, 65536)
		pktlen, raddr, readErr := ln.ReadFromUDP(pktbuf)

		var operr *net.OpError
		if errors.As(readErr, &operr) && operr.Timeout() {
			continue
		}

		log.Printf("Received packet of length %d from %v", pktlen, raddr)

		dh, pkt, parseErr := aurp.ParsePacket(pktbuf[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
		}
		if readErr != nil {
			log.Printf("Failed to read packet: %v", readErr)
			return
		}

		log.Printf("The packet parsed succesfully as a %T", pkt)

		if _, ok := pkt.(*aurp.AppleTalkPacket); ok {
			// Probably something like:
			//
			// * parse the DDP header
			// * check that this is headed for our local network
			// * write the packet out in an EtherTalk frame
			//
			// or maybe if we were implementing a "central hub"
			//
			// * parse the DDP header
			// * see if we know the network
			// * forward to the peer with that network and lowest metric
			log.Print("TODO: handle AppleTalk packets")
		}

		// Existing peer?
		ra := udpAddrFromNet(raddr)
		pr := peers[ra]
		if pr == nil {
			// New peer!
			nextConnID++
			pr = &peer{
				cfg: cfg,
				tr: &aurp.Transport{
					LocalDI:     localDI,
					RemoteDI:    dh.SourceDI, // platinum rule
					LocalConnID: nextConnID,
				},
				conn:  ln,
				raddr: raddr,
				recv:  make(chan aurp.Packet, 1024),
			}
			peers[ra] = pr
			goHandler(pr)
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
