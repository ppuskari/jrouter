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

package zip

import (
	"testing"

	"drjosh.dev/jrouter/atalk/atp"
)

func FuzzUnmarshalPacket(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = UnmarshalPacket(data)
	})
}

func FuzzUnmarshalTReq(f *testing.F) {
	f.Fuzz(func(t *testing.T, xo bool, trti, bitmap uint8, tid uint16, userBytes uint32, data []byte) {
		if len(data) < 4 {
			return
		}
		_, _ = UnmarshalTReq(&atp.TReq{
			ExactlyOnce:          xo,
			TRelTimeoutIndicator: trti,
			Bitmap:               bitmap,
			TransactionID:        tid,
			UserBytes:            [4]byte(data[:4]),
			Data:                 data[4:],
		})
	})
}

func FuzzUnmarshalTResp(f *testing.F) {
	f.Fuzz(func(t *testing.T, eom, sts bool, seq uint8, tid uint16, userBytes uint32, data []byte) {
		if len(data) < 4 {
			return
		}
		_, _ = UnmarshalTResp(&atp.TResp{
			EndOfMessage:          eom,
			SendTransactionStatus: sts,
			Sequence:              seq,
			TransactionID:         tid,
			UserBytes:             [4]byte(data[:4]),
			Data:                  data[4:],
		})
	})
}
