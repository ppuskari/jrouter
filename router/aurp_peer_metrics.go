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

import "github.com/prometheus/client_golang/prometheus"

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
)

func (t *AURPPeerTable) Describe(ch chan<- *prometheus.Desc) {
	ch <- aurpPeerReceiverConnectedDesc
	ch <- aurpPeerSenderConnectedDesc
	ch <- aurpPeerSendRetriesDesc
	ch <- aurpPeerLastHeardFromDesc
	ch <- aurpPeerLastReconnectDesc
	ch <- aurpPeerLastSendDesc
	ch <- aurpPeerLastUpdateDesc
}

func (t *AURPPeerTable) Collect(ch chan<- prometheus.Metric) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, p := range t.peersByIP {
		rconn, sconn := 0, 0
		if p.ReceiverConnected() {
			rconn = 1
		}
		if p.SenderConnected() {
			sconn = 1
		}
		raddr := p.RemoteAddr.String()
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
			float64(p.LastHeardFrom().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastReconnectDesc,
			prometheus.GaugeValue,
			float64(p.LastReconnect().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastSendDesc,
			prometheus.GaugeValue,
			float64(p.LastSend().Unix()),
			raddr,
		)
		ch <- prometheus.MustNewConstMetric(
			aurpPeerLastUpdateDesc,
			prometheus.GaugeValue,
			float64(p.LastUpdate().Unix()),
			raddr,
		)
	}
}
