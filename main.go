package main

import (
	"log"
	"net"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

func main() {
	log.Println("jrouter")

	ln, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 387})
	if err != nil {
		log.Fatalf("Couldn't listen on udp4:387: %v", err)
	}

	// Incoming packet loop
	pb := make([]byte, 65536)
	for {
		plen, _, err := ln.ReadFromUDP(pb)
		if err != nil {
			log.Printf("Failed to read packet: %v", err)
			continue
		}

		_, err = aurp.ParsePacket(pb[:plen])
		if err != nil {
			log.Printf("Failed to parse packet: %v", err)
		}

	}
}
