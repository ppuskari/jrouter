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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// jrouter_aurp_bytes_in_total
	aurpBytesInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aurp", Name: "bytes_in_total",
			Help: "count of AURP bytes received",
		},
		[]string{"peer"},
	)

	// jrouter_aurp_bytes_out_total
	aurpBytesOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aurp", Name: "bytes_out_total",
			Help: "count of AURP bytes sent",
		},
		[]string{"peer"},
	)

	// jrouter_aurp_packets_in_total
	aurpPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aurp", Name: "packets_in_total",
			Help: "count of AURP packets received",
		},
		[]string{"peer"},
	)

	// jrouter_aurp_invalid_packets_in_total
	aurpInvalidPacketsInCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aurp", Name: "invalid_packets_in_total",
			Help: "count of invalid AURP packets received",
		},
		[]string{"peer"},
	)

	// jrouter_aurp_packets_out_total
	aurpPacketsOutCounter = promauto.NewCounterVec(
		prometheus.CounterOpts{Namespace: "jrouter", Subsystem: "aurp", Name: "packets_out_total",
			Help: "count of AURP packets sent",
		},
		[]string{"peer"},
	)
)
