// Copyright 2026 The fhttp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http2

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// throttledSink caps the rate at which a response body is consumed, simulating
// a slow reader: a proxy relaying to a backpressured client, a rate-limited
// download, etc.
type throttledSink struct{ bytesPerSecond int }

func (s throttledSink) Write(p []byte) (int, error) {
	time.Sleep(time.Duration(len(p)) * time.Second / time.Duration(s.bytesPerSecond))
	return len(p), nil
}

// TestTransportSlowReaderLargeResponse verifies that a response body much
// larger than the stream and connection flow-control windows is delivered in
// full when the application consumes it slowly.
//
// Regression test for a WINDOW_UPDATE accounting bug: Read credited the peer
// for buffered-but-unread body bytes (adding cs.bufPipe.Len() where it should
// subtract it), so the advertised stream window desynced from the real one and
// the transfer died partway with "stream error: ...; FLOW_CONTROL_ERROR".
func TestTransportSlowReaderLargeResponse(t *testing.T) {
	const (
		bodySize       = 48 << 20 // 48 MiB, ~3x the 15.6 MiB connection window
		bytesPerSecond = 12 << 20
	)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s := &Server{}
		s.ServeConn(conn, &ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(bodySize))
			w.WriteHeader(200)
			chunk := make([]byte, 1<<20)
			for remain := bodySize; remain > 0; remain -= len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		})})
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transport{
		Settings: map[SettingID]uint32{
			SettingInitialWindowSize: 6291456,
		},
		SettingsOrder:     []SettingID{SettingInitialWindowSize},
		ConnectionFlow:    15663105,
		PseudoHeaderOrder: []string{":method", ":authority", ":scheme", ":path"},
	}
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()

	req, err := http.NewRequest("GET", "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	n, err := io.Copy(throttledSink{bytesPerSecond: bytesPerSecond}, resp.Body)
	if err != nil {
		t.Fatalf("slow read of %d-byte response failed after %d bytes: %v", bodySize, n, err)
	}
	if n != bodySize {
		t.Fatalf("read %d bytes, want %d", n, bodySize)
	}
}
