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

package status

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestSmokeErrorTemplate(t *testing.T) {
	errData := &errorData{
		Operation: "Couldn't fluff the llamas",
		Error:     errors.New("llama comb unavailable"),
		Item: map[string]any{
			"llamas":  "yes",
			"alpacas": 42,
		},
	}

	if err := errorTmpl.Execute(io.Discard, errData); err != nil {
		t.Errorf("errorTmpl.Execute(io.Discard, errData) = %v", err)
	}
}

func TestSmokeStatusTemplate(t *testing.T) {
	data := &statusData{
		Items: map[string]item{
			"Llamas": &simpleItem{
				stat: "✅ Llamas enabled",
			},
			"Alpacas": &templatedItem{
				tmpl: template.Must(template.New("alpacas").Parse("Alpacas enabled at: {{.AlpacasEnabled}}")),
				cb: func(context.Context) (any, error) {
					return struct {
						AlpacasEnabled time.Time
					}{time.Now()}, nil
				},
			},
		},
		PageName:     "Status",
		Version:      "0.0.0",
		Build:        "1234",
		Hostname:     hostname,
		Username:     username,
		ExeName:      exename,
		ExePath:      exepath,
		PID:          os.Getpid(),
		Compiler:     runtime.Compiler,
		RuntimeVer:   runtime.Version(),
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
		StartTime:    startTime.Format(time.RFC1123),
		StartTimeAgo: time.Since(startTime),
		CurrentTime:  time.Now().Format(time.RFC1123),
		Ctx:          context.Background(),
	}

	if err := statusTmpl.Execute(io.Discard, data); err != nil {
		t.Errorf("statusData.Execute(io.Discard, data) = %v", err)
	}
}

func TestSmokeHandle(t *testing.T) {
	ctx := context.Background()
	cctx, setStat, done := AddSimpleItem(ctx, "Llamas")
	defer done()
	setStat("Essence of Llama")

	_, setStat2, done2 := AddSimpleItem(cctx, "Kuzco")
	defer done2()
	setStat2("Oh, right. The poison. The poison for Kuzco, the poison chosen especially to kill Kuzco, Kuzco's poison.")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/status", nil)
	if err != nil {
		t.Fatalf("http.NewReqeustWithContext(GET /status) error = %v", err)
	}
	rec := httptest.NewRecorder()
	Handle(rec, req)
	if got, want := rec.Result().StatusCode, http.StatusOK; got != want {
		t.Errorf("Handle(rec, req): rec.Result().StatusCode = %v, want %v", got, want)
	}
}

func TestCleanHTMLFragmentRemovesPresentationArtifacts(t *testing.T) {
	got := cleanHTMLFragment("\n<td>extended&#x9;</td>\t\n")
	if want := "<td>extended </td>"; got != want {
		t.Fatalf("cleanHTMLFragment() = %q, want %q", got, want)
	}
}

func TestOrderedTopLevelStatusSections(t *testing.T) {
	items := map[string]item{
		"Routing table":      &simpleItem{},
		"AARP on en0":        &simpleItem{},
		"EtherTalk on en0":   &simpleItem{},
		"Something optional": &simpleItem{},
	}
	got := orderedTitles(items, "Status")
	want := []string{
		"EtherTalk on en0",
		"AARP on en0",
		"Routing table",
		"Something optional",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Status order = %v, want %v", got, want)
	}
}

func TestOrderedPeeringSections(t *testing.T) {
	items := map[string]item{
		"RTMP on en0":                      &simpleItem{},
		"Outbound on en0":                  &simpleItem{},
		"Periodically Attempt Connections": &simpleItem{},
		"Inbound on en0":                   &simpleItem{},
		"AURP Peers":                       &simpleItem{},
	}
	got := orderedTitles(items, "Peering")
	want := []string{
		"AURP Peers",
		"Inbound on en0",
		"Outbound on en0",
		"RTMP on en0",
		"Periodically Attempt Connections",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Peering order = %v, want %v", got, want)
	}
}

func TestPeeringContextUsesSeparateRoot(t *testing.T) {
	ctx := PeeringContext(context.Background())
	_, setStatus, done := AddSimpleItem(ctx, "RC2 Peering Test")
	defer done()
	setStatus("ok")

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"/peering",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	HandlePeering(rec, req)
	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("RC2 Peering Test")) {
		t.Fatalf("peering page did not contain registered item")
	}
}
