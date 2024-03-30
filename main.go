package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"

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

	peers := make(map[udpAddr]*peer)
	var nextConnID uint16

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}
	defer ln.Close()
	log.Printf("Listening on %v", ln.LocalAddr())

	log.Println("Press ^C or send SIGINT to stop the router gracefully")
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)

	for _, peerStr := range cfg.Peers {
		if !hasPortRE.MatchString(peerStr) {
			peerStr += ":387"
		}

		raddr, err := net.ResolveUDPAddr("udp4", peerStr)
		if err != nil {
			log.Fatalf("Invalid UDP address: %v", err)
		}
		log.Printf("resolved %q to %v", peerStr, raddr)

		tr := &aurp.Transport{
			LocalDI:     localDI,
			RemoteDI:    aurp.IPDomainIdentifier(raddr.IP),
			LocalConnID: nextConnID,
		}
		nextConnID++

		peer := &peer{
			tr:    tr,
			conn:  ln,
			raddr: raddr,
			recv:  make(chan aurp.Packet, 1024),
		}
		go peer.handle(ctx)

		peers[udpAddrFromNet(raddr)] = peer
	}

	// Incoming packet loop
	for {
		pktbuf := make([]byte, 65536)
		pktlen, raddr, readErr := ln.ReadFromUDP(pktbuf)
		// net.PacketConn.ReadFrom: "Callers should always process
		// the n > 0 bytes returned before considering the error err."

		log.Printf("Received packet of length %d from %v", pktlen, raddr)

		dh, pkt, parseErr := aurp.ParsePacket(pktbuf[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
			continue
		}

		log.Printf("The packet parsed succesfully as a %T", pkt)

		if readErr != nil {
			log.Printf("Failed to read packet: %v", readErr)
			continue
		}

		// Existing peer?
		ra := udpAddrFromNet(raddr)
		pr := peers[ra]
		if pr == nil {
			// New peer!
			nextConnID++
			pr = &peer{
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
			go pr.handle(ctx)
		}

		// Pass the packet to the goroutine in charge of this peer.
		pr.recv <- pkt
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
