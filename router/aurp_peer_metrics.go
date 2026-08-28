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
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	aurpPeerReceiverConnectedDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_receiver_connected",
		"0 if the receiver state for this peer is unconnected, 1 otherwise",
		[]string{"peer"},
		nil,
	)
	aurpPeerSenderConnectedDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_sender_connected",
		"0 if the sender state for this peer is unconnected, 1 otherwise",
		[]string{"peer"},
		nil,
	)
	aurpPeerSendRetriesDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_send_retries",
		"current send retries for each peer",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastHeardFromDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_heard_from_timestamp_seconds",
		"timestamp of lastHeardFrom",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastReconnectDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_reconnect_timestamp_seconds",
		"timestamp of lastReconnect",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastSendDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_send_timestamp_seconds",
		"timestamp of lastSend",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastUpdateDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_update_timestamp_seconds",
		"timestamp of lastUpdate",
		[]string{"peer"},
		nil,
	)
	aurpPeerLastSuccessDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_last_success_timestamp_seconds",
		"timestamp of last completed RI-Rsp exchange",
		[]string{"peer"},
		nil,
	)
	aurpPeerReconnectFailuresDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_reconnect_failures",
		"consecutive failed receiver connection attempts",
		[]string{"peer"},
		nil,
	)
	aurpPeerNextReconnectDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_next_reconnect_timestamp_seconds",
		"earliest scheduled reconnect time, or 0 when immediately eligible",
		[]string{"peer"},
		nil,
	)
	aurpPeerDuplicateRoutingDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_duplicate_routing_packets_total",
		"routing packets received with sequence n-1",
		[]string{"peer"},
		nil,
	)
	aurpPeerReacksSentDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_reacks_sent_total",
		"RI-Acks retransmitted for duplicate routing packets",
		[]string{"peer"},
		nil,
	)
	aurpPeerStaleRoutingDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_stale_routing_packets_total",
		"stale routing packets dropped outside n-1/n/n+1",
		[]string{"peer"},
		nil,
	)
	aurpPeerFutureRoutingDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_future_routing_packets_total",
		"routing packets received with sequence n+1",
		[]string{"peer"},
		nil,
	)
	aurpPeerConnIDMismatchDesc = prometheus.NewDesc(
		"jrouter_aurp_peer_connection_id_mismatches_total",
		"routing packets dropped for connection ID mismatch",
		[]string{"peer"},
		nil,
	)
	aurpConfiguredPeerDNSFailuresDesc = prometheus.NewDesc(
		"jrouter_aurp_configured_peer_dns_failures",
		"consecutive DNS resolution failures for a configured peer",
		[]string{"configured_peer"},
		nil,
	)
	aurpConfiguredPeerNextDNSDesc = prometheus.NewDesc(
		"jrouter_aurp_configured_peer_next_dns_retry_timestamp_seconds",
		"next DNS retry time for a configured peer, or 0 when immediately eligible",
		[]string{"configured_peer"},
		nil,
	)
)

func (t *AURPPeerTable) Describe(ch chan<- *prometheus.Desc) {
	ch <- aurpPeerReceiverConnectedDesc
	ch <- aurpPeerSenderConnectedDesc
	ch <- aurpPeerSendRetriesDesc
	ch <- aurpPeerLastHeardFromDesc
	ch <- aurpPeerLastReconnectDesc
	ch <- aurpPeerLastSendDesc
	ch <- aurpPeerLastUpdateDesc
	ch <- aurpPeerLastSuccessDesc
	ch <- aurpPeerReconnectFailuresDesc
	ch <- aurpPeerNextReconnectDesc
	ch <- aurpPeerDuplicateRoutingDesc
	ch <- aurpPeerReacksSentDesc
	ch <- aurpPeerStaleRoutingDesc
	ch <- aurpPeerFutureRoutingDesc
	ch <- aurpPeerConnIDMismatchDesc
	ch <- aurpConfiguredPeerDNSFailuresDesc
	ch <- aurpConfiguredPeerNextDNSDesc
}

func metricTimestamp(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.Unix())
}

func (t *AURPPeerTable) Collect(ch chan<- prometheus.Metric) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, p := range t.uniquePeersLocked() {
		rconn, sconn := 0, 0
		if p.ReceiverConnected() {
			rconn = 1
		}
		if p.SenderConnected() {
			sconn = 1
		}
		raddr := p.RemoteAddrString()
		ch <- prometheus.MustNewConstMetric(
			aurpPeerReceiverConnectedDesc,
			prometheus.GaugeValue,
			float64(rconn),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerSenderConnectedDesc,
			prometheus.GaugeValue,
			float64(sconn),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerSendRetriesDesc,
			prometheus.GaugeValue,
			float64(p.SendRetries()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastHeardFromDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.LastHeardFrom()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastReconnectDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.LastReconnect()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastSendDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.LastSend()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastUpdateDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.LastUpdate()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastSuccessDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.LastSuccess()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerReconnectFailuresDesc,
			prometheus.GaugeValue,
			float64(p.ReconnectFailures()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerNextReconnectDesc,
			prometheus.GaugeValue,
			metricTimestamp(p.NextReconnect()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerDuplicateRoutingDesc,
			prometheus.CounterValue,
			float64(p.DuplicateRoutingPackets()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerReacksSentDesc,
			prometheus.CounterValue,
			float64(p.ReacksSent()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerStaleRoutingDesc,
			prometheus.CounterValue,
			float64(p.StaleRoutingPackets()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerFutureRoutingDesc,
			prometheus.CounterValue,
			float64(p.FutureRoutingPackets()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerConnIDMismatchDesc,
			prometheus.CounterValue,
			float64(p.ConnectionIDMismatches()),
			raddr,
		)
	}

	for configuredPeer, dns := range t.dnsByConfigured {
		ch <- prometheus.MustNewConstMetric(
			aurpConfiguredPeerDNSFailuresDesc,
			prometheus.GaugeValue,
			float64(dns.failures),
			configuredPeer,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpConfiguredPeerNextDNSDesc,
			prometheus.GaugeValue,
			metricTimestamp(dns.next),
			configuredPeer,
		)
	}
}
