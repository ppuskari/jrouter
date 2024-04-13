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

import "github.com/sfiera/multitalk/pkg/ddp"

type QueryPacket struct {
	Function Function // 1
	// NetworkCount uint8
	Networks []ddp.Network
}

type ReplyPacket struct {
	Function Function // 2 or 8
	// NetworkCount uint8
	Tuples []ZoneTuple
}

type ZoneTuple struct {
	Network  ddp.Network
	ZoneName string
}
