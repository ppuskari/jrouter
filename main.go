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
		pktlen, _, readErr := ln.ReadFromUDP(pb)
		// "Callers should always process
		// the n > 0 bytes returned before considering the error err."

		_, parseErr := aurp.ParsePacket(pb[:pktlen])
		if parseErr != nil {
			log.Printf("Failed to parse packet: %v", parseErr)
		}

		if readErr != nil {
			log.Printf("Failed to read packet: %v", readErr)
			continue
		}
	}
}
