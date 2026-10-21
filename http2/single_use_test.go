package http2_test

import (
	"io"
	"net"
	stdhttp "net/http"
	stdhttptest "net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2"
	tls "github.com/bogdanfinn/utls"
)

// A Connection: close request gets a single-use connection. That connection
// used to count as spent as soon as its next stream id passed 1, which is
// already true before any request when the first stream is not 1, as with
// InitialStreamID or PRIORITY frames. The request was then never sent and the
// transport dialed a new connection over and over.
func TestSingleUseConnServesItsRequestWhenTheFirstStreamIsNotOne(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*http2.Transport)
		close     bool
	}{
		{name: "first stream 1, Connection: close", close: true},
		{name: "InitialStreamID 3, keep-alive", configure: func(tr *http2.Transport) { tr.InitialStreamID = 3 }},
		{name: "InitialStreamID 3, Connection: close", configure: func(tr *http2.Transport) { tr.InitialStreamID = 3 }, close: true},
		{name: "PRIORITY frames, Connection: close", configure: func(tr *http2.Transport) {
			tr.Priorities = []http2.Priority{
				{StreamID: 3, PriorityParam: http2.PriorityParam{Weight: 200}},
				{StreamID: 5, PriorityParam: http2.PriorityParam{Weight: 100}},
			}
		}, close: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var conns atomic.Int32
			srv := stdhttptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				io.WriteString(w, r.Proto)
			}))
			srv.EnableHTTP2 = true
			srv.Config.ConnState = func(_ net.Conn, s stdhttp.ConnState) {
				if s == stdhttp.StateNew {
					conns.Add(1)
				}
			}
			srv.StartTLS()
			defer srv.Close()

			tr := &http2.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			if c.configure != nil {
				c.configure(tr)
			}
			defer tr.CloseIdleConnections()

			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Close = c.close

			type result struct {
				body string
				err  error
			}
			done := make(chan result, 1)
			go func() {
				res, err := tr.RoundTrip(req)
				if err != nil {
					done <- result{err: err}
					return
				}
				defer res.Body.Close()
				b, err := io.ReadAll(res.Body)
				done <- result{body: string(b), err: err}
			}()

			select {
			case r := <-done:
				if r.err != nil {
					t.Fatalf("round trip: %v", r.err)
				}
				if r.body != "HTTP/2.0" {
					t.Fatalf("body = %q, want HTTP/2.0", r.body)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no response after 5s, %d connections dialed", conns.Load())
			}

			if n := conns.Load(); n != 1 {
				t.Fatalf("dialed %d connections for one request, want 1", n)
			}
		})
	}
}
