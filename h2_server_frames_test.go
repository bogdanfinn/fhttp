//go:build !nethttpomithttp2

package http_test

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	. "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/fhttp/http2/hpack"
	"github.com/bogdanfinn/fhttp/httptest"
)

// h2Servers set up a Server with each HTTP/2 server implementation: the one
// bundled in h2_bundle.go and the http2 package's.
var h2Servers = []struct {
	name      string
	configure func(*Server)
}{
	{"bundled", func(s *Server) { ExportHttp2ConfigureServer(s, nil) }},
	{"http2", func(s *Server) { http2.ConfigureServer(s, nil) }},
}

// h2Client is a raw HTTP/2 client connection, for frames a real client would
// not send.
type h2Client struct {
	t   *testing.T
	fr  *http2.Framer
	buf bytes.Buffer
	enc *hpack.Encoder
}

func newH2Client(t *testing.T, configure func(*Server), h HandlerFunc, settings ...http2.Setting) *h2Client {
	t.Helper()
	ts := httptest.NewUnstartedServer(h)
	configure(ts.Config)
	ts.TLS = ts.Config.TLSConfig
	ts.StartTLS()
	t.Cleanup(ts.Close)

	conn, err := tls.Dial("tcp", ts.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatal(err)
	}
	c := &h2Client{t: t, fr: http2.NewFramer(conn, conn)}
	c.enc = hpack.NewEncoder(&c.buf)
	if err := c.fr.WriteSettings(settings...); err != nil {
		t.Fatal(err)
	}
	for {
		f, err := c.fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if sf, ok := f.(*http2.SettingsFrame); ok && !sf.IsAck() {
			break
		}
	}
	if err := c.fr.WriteSettingsAck(); err != nil {
		t.Fatal(err)
	}
	return c
}

// writeRequest sends a GET request's headers on the stream, ending the
// stream unless the request is to have a body.
func (c *h2Client) writeRequest(streamID uint32, endStream bool) {
	c.t.Helper()
	c.buf.Reset()
	for _, f := range [][2]string{{":method", "GET"}, {":scheme", "https"}, {":authority", "example.com"}, {":path", "/"}} {
		c.enc.WriteField(hpack.HeaderField{Name: f[0], Value: f[1]})
	}
	if err := c.fr.WriteHeaders(http2.HeadersFrameParam{StreamID: streamID, BlockFragment: c.buf.Bytes(), EndStream: endStream, EndHeaders: true}); err != nil {
		c.t.Fatal(err)
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

// A stream error must cancel the handler's request context (golang.org/issue/67036).
func TestH2ServerCancelsHandlerOnStreamError(t *testing.T) {
	for _, srv := range h2Servers {
		t.Run(srv.name, func(t *testing.T) {
			started, done := make(chan struct{}), make(chan struct{})
			c := newH2Client(t, srv.configure, func(w ResponseWriter, r *Request) {
				close(started)
				<-r.Context().Done()
				close(done)
			})
			c.writeRequest(1, true)
			waitFor(t, started, "handler did not start")
			// A second set of request headers on the stream is a stream error.
			c.writeRequest(1, true)
			waitFor(t, done, "handler context was not canceled after the stream error")
		})
	}
}

// A write deadline must release a handler whose write is blocked on flow
// control, and cancel its request context.
func TestH2ServerWriteDeadlineUnblocksFlowControlledWrite(t *testing.T) {
	for _, srv := range h2Servers {
		t.Run(srv.name, func(t *testing.T) {
			errc, done := make(chan error, 1), make(chan struct{})
			// The client grants no flow-control window, so the handler's write blocks.
			c := newH2Client(t, srv.configure, func(w ResponseWriter, r *Request) {
				defer close(done)
				if err := NewResponseController(w).SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
					errc <- err
					return
				}
				_, err := w.Write(make([]byte, 32<<10))
				errc <- err
				<-r.Context().Done()
			}, http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0})
			c.writeRequest(1, true)
			select {
			case err := <-errc:
				if err == nil || errors.Is(err, ErrNotSupported) {
					t.Fatalf("blocked Write returned %v after the write deadline, want a write error", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocked Write did not return after the write deadline")
			}
			waitFor(t, done, "handler context was not canceled after the write deadline")
		})
	}
}

// A read deadline, in the future or already past, must release a handler
// waiting for a request body that never comes.
func TestH2ServerReadDeadline(t *testing.T) {
	for _, srv := range h2Servers {
		for _, d := range []time.Duration{50 * time.Millisecond, -time.Second} {
			t.Run(fmt.Sprintf("%s/%v", srv.name, d), func(t *testing.T) {
				errc := make(chan error, 1)
				c := newH2Client(t, srv.configure, func(w ResponseWriter, r *Request) {
					if err := NewResponseController(w).SetReadDeadline(time.Now().Add(d)); err != nil {
						errc <- err
						return
					}
					_, err := io.ReadAll(r.Body)
					errc <- err
				})
				c.writeRequest(1, false) // the body never comes
				select {
				case err := <-errc:
					if !errors.Is(err, os.ErrDeadlineExceeded) {
						t.Fatalf("reading the body returned %v, want os.ErrDeadlineExceeded", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("reading the body did not return after the read deadline")
				}
			})
		}
	}
}

// A read deadline on a request without a body must not panic when it fires
// (golang.org/issue/58237).
func TestH2ServerReadDeadlineWithoutBody(t *testing.T) {
	for _, srv := range h2Servers {
		t.Run(srv.name, func(t *testing.T) {
			done := make(chan struct{})
			c := newH2Client(t, srv.configure, func(w ResponseWriter, r *Request) {
				defer close(done)
				NewResponseController(w).SetReadDeadline(time.Now().Add(time.Millisecond))
				time.Sleep(10 * time.Millisecond)
			})
			c.writeRequest(1, true)
			waitFor(t, done, "handler did not finish")
		})
	}
}
