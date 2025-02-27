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

const routingTableTemplate = `
<table>
	<thead><tr>
		<th>Network range</th>
		<th>Extended?</th>
		<th>Zone names</th>
		<th>Distance</th>
		<th>Last seen</th>
		<th>Target</th>
	</tr></thead>
	<tbody>
{{range $route := . }}
	<tr>
		<td>{{$route.NetStart}}{{if not (eq $route.NetStart $route.NetEnd)}} - {{$route.NetEnd}}{{end}}</td>
		<td>{{if $route.Extended}}✅{{else}}-{{end}}</td>
		<td>{{range $route.ZoneNames.ToSlice}}{{.}}<br>{{end}}</td>
		<td>{{$route.Distance}}</td>
		<td>{{$route.LastSeenAgo}}</td>
		<td>{{$route.Target}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`

const peerTableTemplate = `
<table>
	<thead><tr>
		<th>Configured addr</th>
		<th>Remote addr</th>
		<th>Receiver state</th>
		<th>Sender state</th>
		<th>Last heard from</th>
		<th>Last reconnect</th>
		<th>Last update</th>
		<th>Last send</th>
		<th>Send retries</th>
	</tr></thead>
	<tbody>
{{range $peer := . }}
	<tr>
		<td>{{$peer.ConfiguredAddr}}</td>
		<td>{{$peer.RemoteAddr}}</td>
		<td>{{$peer.ReceiverState}}</td>
		<td>{{$peer.SenderState}}</td>
		<td>{{$peer.LastHeardFromAgo}}</td>
		<td>{{$peer.LastReconnectAgo}}</td>
		<td>{{$peer.LastUpdateAgo}}</td>
		<td>{{$peer.LastSendAgo}}</td>
		<td>{{$peer.SendRetries}}</td>
	</tr>
{{end}}
	</tbody>
</table>
`
