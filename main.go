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
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
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

	localDI := aurp.IPDomainIdentifier(net.ParseIP(cfg.LocalIP).To4())
	if localDI == nil {
		localDI = defaultDomainIdentifier(logger)
	}
	if localDI == nil {
		logger.Error("No global unicast IPv4 addresses on any network interfaces, and no valid local_ip address in configuration")
		os.Exit(1)
	}

	logger.Debug("Starting up", "local_domain_identifier", localDI, "ethertalk-config", cfg.EtherTalk)

	// ----------------------------- UDP listener -----------------------------
	//
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(cfg.ListenPort)})
	if err != nil {
		logger.Error("AURP: Couldn't listen on udp4", "port", cfg.ListenPort, "error", err)
		os.Exit(1)
	}
	defer udpConn.Close()
	logger.Info("AURP: listening", "localaddr", udpConn.LocalAddr())

	// ---------------------------- Ctrl-C handling ---------------------------
	//
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM is what Docker sends the container process to let it clean up.
	// Fortunately syscall.SIGTERM is defined even when GOOS=windows.
	logger.Info("Press ^C or send SIGINT or SIGTERM to stop the router gracefully")
	ctx, _ := signal.NotifyContext(cctx, os.Interrupt, syscall.SIGTERM)

	// -------------------------------- Router --------------------------------
	//
	rooter := &router.Router{
		Logger:     logger,
		Config:     cfg,
		RouteTable: router.NewRouteTable(ctx),
		AURPPeers:  router.NewAURPPeerTable(ctx, logger),
		Identity:   localDI,
	}

	// --------------------------------- HTTP ---------------------------------
	//
	if cfg.MonitoringAddr == "" {
		logger.Warn("monitoring_addr is empty - disabling the monitoring HTTP server")
	} else {
		http.Handle("/chatlog/{ip}", rooter.AURPPeers)
		http.HandleFunc("/status", status.Handle)
		http.Handle("/metrics", promhttp.Handler())
		http.Handle("/", http.FileServerFS(status.StaticFiles))
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
	// Each port is created with its own pcap handle.
	createEtherTalkPorts(logger, rooter)

	// -------------------------------- Peers ---------------------------------
	// Fetch the peer list from the URL (if configured), then resolve them all
	// to IPv4 addresses.
	fetchPeerListURL(logger, rooter)
	resolvePeerHostnames(ctx, logger, rooter, udpConn)

	// -------------------------- Run all the things! -------------------------
	// main blocks on this waitgroup before exiting the program
	//
	wg := new(sync.WaitGroup)

	// -------------------------- Run EtherTalk ports -------------------------
	//
	for _, etPort := range rooter.Ports {
		ctx := etPort.StatusCtx(ctx)

		// Run AARP and RTMP on each port.
		go etPort.RunAARP(ctx)
		go etPort.RunRTMP(ctx)

		// Start handling packets.
		wg.Go(func() { etPort.Serve(ctx) })
		wg.Go(func() { etPort.Outbox(ctx) })
	}

	// ------------------------------- Run AURP -------------------------------
	// This happens after adding local networks to the routing table, so that
	// we have networks to advertise to peers before connecting to them.
	wg.Go(func() { rooter.AURPInput(ctx, logger, wg, udpConn) })
	wg.Go(func() { rooter.AURPPeers.PeriodicallyAttemptConnections(ctx, logger, wg) })

	// Among other things, peer handlers send outbound Open-Reqs, initiating
	// outbound connections.
	rooter.AURPPeers.RunAll(ctx, wg)

	// Block until the various goroutines have all returned.
	wg.Wait()
}

func defaultDomainIdentifier(logger *slog.Logger) aurp.IPDomainIdentifier {
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

		ip := inet.IP.To4()
		if ip == nil {
			continue
		}
		return aurp.IPDomainIdentifier(ip)
	}
	return nil
}

func createEtherTalkPorts(logger *slog.Logger, rooter *router.Router) {
	for _, etcfg := range rooter.Config.EtherTalk {
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
		// Do not close the pcap handle here. The EtherTalkPort owns it for
		// the lifetime of the router; closing it when this helper returns leaves
		// AARP/RTMP/Serve with a dead native handle and can crash in
		// pcap_sendpacket on the first AARP probe.
		zones := router.MakeSet(etcfg.DefaultZoneName)
		zones.Insert(etcfg.ExtraZones...)

		rooter.NewEtherTalkPort(
			etcfg.Device,
			myHWAddr,
			etcfg.NetStart,
			etcfg.NetEnd,
			etcfg.DefaultZoneName,
			zones,
			handle,
		)
	}
}

func fetchPeerListURL(logger *slog.Logger, rooter *router.Router) {
	if rooter.Config.PeerListURL == "" {
		return
	}
	logger.Info("Fetching peer list", "peerlist-url", rooter.Config.PeerListURL)
	existing := len(rooter.Config.Peers)
	func() {
		resp, err := http.Get(rooter.Config.PeerListURL)
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
			rooter.Config.Peers = append(rooter.Config.Peers, p)
		}
		if err := sc.Err(); err != nil {
			logger.Error("Couldn't scan peer list response", "peerlist-url", rooter.Config.PeerListURL, "error", err)
			os.Exit(1)
		}
	}()
	logger.Info("Fetched list", "length", len(rooter.Config.Peers)-existing)
}

// resolvePeerHostnames resolves all configured peer hostnames to all current
// IPv4 candidates. A configured hostname remains one logical AURP peer even
// when DNS returns multiple A records.
func resolvePeerHostnames(ctx context.Context, logger *slog.Logger, rooter *router.Router, udpConn *net.UDPConn) {
	var resolverWG sync.WaitGroup
	peerCh := make(chan string)
	for range runtime.GOMAXPROCS(0) {
		resolverWG.Go(func() {
			for {
				var peerStr string
				select {
				case <-ctx.Done():
					return
				case peerStr = <-peerCh:
				}

				if peerStr == "" {
					return
				}

				addrs, err := net.LookupIP(peerStr)
				if err != nil {
					logger.Warn("Couldn't resolve address, skipping", "configured-addr", peerStr, "error", err)
					continue
				}

				added := 0
				for _, raddr := range addrs {
					raddr4 := raddr.To4()
					if raddr4 == nil {
						continue
					}
					if raddr4.Equal(net.IP(rooter.Identity)) {
						logger.Debug("Not adding self as peer", "configured-addr", peerStr, "raddr", raddr4)
						continue
					}

					logger.Debug("Resolved address", "configured-addr", peerStr, "raddr", raddr4)
					if _, err := rooter.AURPPeers.LookupOrCreate(
						ctx,
						logger,
						rooter.RouteTable,
						udpConn,
						peerStr,
						raddr4,
						rooter.Identity,
						nil,
					); err != nil {
						logger.Warn("AURP: peer create", "configured-addr", peerStr, "raddr", raddr4, "error", err)
						continue
					}
					added++
				}
				if added == 0 {
					logger.Warn("Resolved peer has no usable IPv4 addresses, skipping", "configured-addr", peerStr)
				}
			}
		})
	}

	for _, peerStr := range rooter.Config.Peers {
		if peerStr == "" {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case peerCh <- peerStr:
		}
	}
	close(peerCh)
	resolverWG.Wait()
}
