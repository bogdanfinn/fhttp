package http2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2/hpack"
)

// A request canceled while its response body is still unread must reset the
// stream at once, as golang.org/x/net/http2 does. With the condition in
// cancelStream inverted the stream was neither reset nor forgotten until the
// caller closed the body, so the server kept sending into it and the stream
// kept one of the connection's concurrent stream slots.
func TestTransportCancelResetsStreamWithUnreadBody(t *testing.T) {
	ct := newClientTester(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan struct{})

	ct.client = func() error {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://dummy.tld/", nil)
		res, err := ct.tr.RoundTrip(req)
		if err != nil {
			return err
		}
		buf := make([]byte, 1)
		if _, err := res.Body.Read(buf); err != nil {
			return fmt.Errorf("first body read: %v", err)
		}
		cancel()
		// The body is deliberately not closed until the server saw the reset.
		<-serverDone
		res.Body.Close()

		return nil
	}
	ct.server = func() error {
		defer close(serverDone)
		ct.greet()
		var hf *HeadersFrame
		for hf == nil {
			f, err := ct.fr.ReadFrame()
			if err != nil {
				return fmt.Errorf("ReadFrame while waiting for Headers: %v", err)
			}
			hf, _ = f.(*HeadersFrame)
		}
		var buf bytes.Buffer
		enc := hpack.NewEncoder(&buf)
		enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
		ct.fr.WriteHeaders(HeadersFrameParam{StreamID: hf.StreamID, EndHeaders: true, BlockFragment: buf.Bytes()})
		ct.fr.WriteData(hf.StreamID, false, make([]byte, 100))

		deadline := time.After(5 * time.Second)
		frames := make(chan Frame)
		errc := make(chan error, 1)
		go func() {
			for {
				f, err := ct.fr.ReadFrame()
				if err != nil {
					errc <- err
					return
				}
				frames <- f
			}
		}()
		for {
			select {
			case f := <-frames:
				if rf, ok := f.(*RSTStreamFrame); ok && rf.StreamID == hf.StreamID {
					if rf.ErrCode != ErrCodeCancel {
						return fmt.Errorf("RST_STREAM code = %v, want CANCEL", rf.ErrCode)
					}
					return nil
				}
			case err := <-errc:
				return fmt.Errorf("ReadFrame while waiting for RST_STREAM: %v", err)
			case <-deadline:
				return errors.New("no RST_STREAM within 5s of canceling a request whose body is unread")
			}
		}
	}
	ct.run()
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) { panic("hpack: forced encoder panic") }

// A panic while encoding request headers must not leave cc.mu locked, which
// wedged the connection for good. The request fails with errClientConnUnusable
// (RoundTrip retries those on another connection) and the connection takes no
// new requests, since its HPACK state no longer matches the peer's.
func TestClientConnEncodeHeadersPanicReleasesConn(t *testing.T) {
	ct := newClientTester(t)
	ct.client = func() error {
		cc, err := ct.tr.NewClientConn(ct.cc)
		if err != nil {
			return err
		}
		cc.mu.Lock()
		cc.henc = hpack.NewEncoder(panicWriter{})
		cc.mu.Unlock()

		req, _ := http.NewRequest("GET", "https://dummy.tld/", nil)
		done := make(chan error, 1)
		go func() {
			_, err := cc.RoundTrip(req)
			done <- err
		}()
		select {
		case err := <-done:
			if err != errClientConnUnusable {
				return fmt.Errorf("RoundTrip error = %v, want errClientConnUnusable", err)
			}
		case <-time.After(5 * time.Second):
			return errors.New("RoundTrip did not return after the encoder panicked")
		}
		if !cc.mu.TryLock() {
			return errors.New("cc.mu still held after the encoder panicked")
		}
		cc.mu.Unlock()
		if cc.CanTakeNewRequest() {
			return errors.New("connection still takes new requests after the encoder panicked")
		}

		return nil
	}
	ct.server = func() error {
		ct.greet()
		for {
			if _, err := ct.fr.ReadFrame(); err != nil {
				return nil
			}
		}
	}
	ct.run()
}
