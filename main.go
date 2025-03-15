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
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"drjosh.dev/jrouter/aurp"
	"drjosh.dev/jrouter/meta"
	"drjosh.dev/jrouter/router"
	"drjosh.dev/jrouter/status"

	"github.com/google/gopacket/pcap"
	"github.com/lmittmann/tint"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sfiera/multitalk/pkg/ethernet"
)

var (
	configFilePath = flag.String("config", "jrouter.yaml", "Path to configuration file to use")
	verbose        = flag.Bool("v", false, "Enables debug logs")
	noColour       = flag.Bool("no-colour", false, "Disables colour in log output")
	version        = flag.Bool("version", false, "Prints the program version and exits")
)

func main() {
	// For some reason it occasionally panics and the panics have no traceback?
	// This didn't help:
	// debug.SetTraceback("all")
	// I think some dependency is calling recover in a defer too broadly.

	flag.Parse()

	if *version {
		fmt.Println(meta.NameVersion)
		return
	}

	// -------------------------------- Logger --------------------------------
	//
	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(tint.NewHandler(os.Stderr, &tint.Options{
		NoColor: *noColour,
		Level:   logLevel,
	}))

	logger.Info(meta.NameVersion)

	// -------------------------------- Config --------------------------------
	//
	cfg, err := router.LoadConfig(*configFilePath)
	if err != nil {
		logger.Error("Couldn't load configuration file", "error", err)
		os.Exit(1)
	}

	localIP := net.ParseIP(cfg.LocalIP).To4()
	if localIP == nil {
		iaddrs, err := net.InterfaceAddrs()
		if err != nil {
			logger.Error("Couldn't read network interface addresses", "error", err)
			os.Exit(1)
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
			logger.Error("No global unicast IPv4 addresses on any network interfaces, and no valid local_ip address in configuration")
			os.Exit(1)
		}
	}
	localDI := aurp.IPDomainIdentifier(localIP)

	logger.Debug("Starting up", "localIP", localIP, "ethertalk-config", cfg.EtherTalk)

	// ----------------------------- UDP listener -----------------------------
	//
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		logger.Error("AURP: Couldn't listen on udp4", "port", cfg.ListenPort, "error", err)
		os.Exit(1)
	}
	defer udpConn.Close()
	logger.Info("AURP: listening", "localaddr", udpConn.LocalAddr())

	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM is what Docker sends the container process to let it clean up.
	// Fortunately syscall.SIGTERM is defined even when GOOS=windows.
	logger.Info("Press ^C or send SIGINT or SIGTERM to stop the router gracefully")
	ctx, _ := signal.NotifyContext(cctx, os.Interrupt, syscall.SIGTERM)

	// --------------------------------- HTTP ---------------------------------
	//
	if cfg.MonitoringAddr == "" {
		logger.Warn("monitoring_addr is empty - disabling the monitoring HTTP server")
	} else {
		http.HandleFunc("/status", status.Handle)
		http.Handle("/metrics", promhttp.Handler())
		go func() {
			err := http.ListenAndServe(cfg.MonitoringAddr, nil)
			logger.Error("http.ListenAndServe", "error", err)
		}()
	}

	// --------------------------------- Pcap ---------------------------------
	//
	if len(cfg.EtherTalk) == 0 {
		logger.Error("The ethertalk config in jrouter.yaml was empty; at least one entry is required")
		os.Exit(1)
	}

	var ethertalkPorts []*router.EtherTalkPort

	for _, etcfg := range cfg.EtherTalk {
		// First check the interface
		iface, err := net.InterfaceByName(etcfg.Device)
		if err != nil {
			logger.Error("Couldn't find interface", "device", etcfg.Device, "error", err)
			os.Exit(1)
		}

		myHWAddr := ethernet.Addr(iface.HardwareAddr)
		if etcfg.EthAddr != "" {
			// Override myHWAddr with the configured address
			netHWAddr, err := net.ParseMAC(etcfg.EthAddr)
			if err != nil {
				logger.Error("Couldn't parse ethertalk.ethernet_addr value", "ethernet_addr", etcfg.EthAddr, "error", err)
				os.Exit(1)
			}
			myHWAddr = ethernet.Addr(netHWAddr)
		}

		handle, err := pcap.OpenLive(etcfg.Device, 4096, true, 100*time.Millisecond)
		if err != nil {
			logger.Error("Couldn't open device for packet capture", "device", etcfg.Device, "error", err)
			os.Exit(1)
		}
		bpfFilter := fmt.Sprintf("(atalk or aarp) and (ether multicast or ether dst %s)", myHWAddr)
		if err := handle.SetBPFFilter(bpfFilter); err != nil {
			handle.Close()
			logger.Error("Couldn't set BPF filter on packet capture", "error", err)
			os.Exit(1)
		}
		defer handle.Close()

		ethertalkPorts = append(ethertalkPorts, &router.EtherTalkPort{
			Logger:          logger,
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
	//
	routes := router.NewRouteTable()
	status.AddItem(ctx, "Routing table", routingTableTemplate, func(context.Context) (any, error) {
		rs := routes.Dump()
		slices.SortFunc(rs, func(ra, rb router.Route) int {
			return cmp.Compare(ra.NetStart, rb.NetStart)
		})
		return rs, nil
	})

	// -------------------------------- Peers ---------------------------------
	//
	aurpPeers := router.NewAURPPeerTable(ctx)

	if cfg.PeerListURL != "" {
		logger.Info("Fetching peer list", "peerlist-url", cfg.PeerListURL)
		existing := len(cfg.Peers)
		func() {
			resp, err := http.Get(cfg.PeerListURL)
			if err != nil {
				logger.Error("Couldn't fetch peer list", "error", err)
				os.Exit(1)
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
				logger.Error("Couldn't scan peer list response", "peerlist-url", cfg.PeerListURL, "error", err)
				os.Exit(1)
			}
		}()
		logger.Info("Fetched list", "length", len(cfg.Peers)-existing)
	}

	wg := new(sync.WaitGroup)
	defer wg.Wait()

	for _, peerStr := range cfg.Peers {
		raddr, err := net.ResolveIPAddr("ip4", peerStr)
		if err != nil {
			logger.Warn("Couldn't resolve address, skipping", "error", err)
			continue
		}
		logger.Debug("Resolved address", "configured-addr", peerStr, "raddr", raddr)

		// Conversion using To4 is necessary so that peers don't all collide in
		// the peersByIP map.
		raddr4 := raddr.IP.To4()
		if raddr4 == nil {
			logger.Warn("Resolved peer address is not an IPv4 address, skipping", "configured-addr", peerStr, "raddr", raddr)
			continue
		}

		if raddr4.Equal(localIP) {
			logger.Debug("Not adding self as peer", "configured-addr", peerStr, "raddr", raddr)
			continue
		}

		if _, err := aurpPeers.LookupOrCreate(ctx, logger, wg, routes, udpConn, peerStr, raddr4, localDI, nil); err != nil {
			logger.Warn("AURP: peer create", "error", err)
			continue
		}
	}

	// -------------------------------- Router --------------------------------
	//
	rooter := &router.Router{
		Logger:     logger,
		Config:     cfg,
		RouteTable: routes,
		AURPPeers:  aurpPeers,
	}

	// Attach ports to router
	rooter.Ports = append(rooter.Ports, ethertalkPorts...)
	for _, etPort := range ethertalkPorts {
		// Attach router to port
		etPort.Router = rooter

		// Add port to routing table
		if _, err := routes.UpsertRoute(etPort, true /* extended */, etPort.NetStart, etPort.NetEnd, 0); err != nil {
			logger.Error("Couldn't create route for EtherTalk port", "error", err)
			os.Exit(1)
		}
		if err := routes.AddZonesToNetwork(etPort.NetStart, etPort.AvailableZones.ToSlice()...); err != nil {
			logger.Error("Couldn't add zones to route that was just created", "error", err)
			os.Exit(1)
		}

		// Run AARP and RTMP on each port.
		etPort.AARPMachine = router.NewAARPMachine(logger, etPort, etPort.EthernetAddr)
		go etPort.AARPMachine.Run(ctx)
		go etPort.RunRTMP(ctx)

		// Finally, start handling packets.
		wg.Add(1)
		go etPort.Serve(ctx, wg)
	}

	// ----------------------------- AURP inbound -----------------------------
	//
	wg.Add(1)
	go rooter.AURPInput(ctx, logger, wg, cfg, udpConn, localDI)

}
