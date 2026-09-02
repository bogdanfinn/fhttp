// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// White-box tests for transport.go (in package http instead of http_test).

package http

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"strings"
	"sync"
	"testing"

	tls "github.com/bogdanfinn/utls"
	"github.com/klauspost/compress/gzip"

	"github.com/bogdanfinn/fhttp/internal"
)

// Issue 15446: incorrect wrapping of errors when server closes an idle connection.
func TestTransportPersistConnReadLoopEOF(t *testing.T) {
	ln := newLocalListener(t)
	defer ln.Close()

	connc := make(chan net.Conn, 1)
	go func() {
		defer close(connc)
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		connc <- c
	}()

	tr := new(Transport)
	req, _ := NewRequest("GET", "http://"+ln.Addr().String(), nil)
	req = req.WithT(t)
	treq := &transportRequest{Request: req}
	cm := connectMethod{targetScheme: "http", targetAddr: ln.Addr().String()}
	pc, err := tr.getConn(treq, cm)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.close(errors.New("test over"))

	conn := <-connc
	if conn == nil {
		// Already called t.Error in the accept goroutine.
		return
	}
	conn.Close() // simulate the server hanging up on the client

	_, err = pc.roundTrip(treq)
	if !isTransportReadFromServerError(err) && err != errServerClosedIdle {
		t.Errorf("roundTrip = %#v, %v; want errServerClosedIdle or transportReadFromServerError", err, err)
	}

	<-pc.closech
	err = pc.closed
	if !isTransportReadFromServerError(err) && err != errServerClosedIdle {
		t.Errorf("pc.closed = %#v, %v; want errServerClosedIdle or transportReadFromServerError", err, err)
	}
}

func isTransportReadFromServerError(err error) bool {
	_, ok := err.(transportReadFromServerError)
	return ok
}

func newLocalListener(t *testing.T) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ln, err = net.Listen("tcp6", "[::1]:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func dummyRequest(method string) *Request {
	req, err := NewRequest(method, "http://fake.tld/", nil)
	if err != nil {
		panic(err)
	}
	return req
}
func dummyRequestWithBody(method string) *Request {
	req, err := NewRequest(method, "http://fake.tld/", strings.NewReader("foo"))
	if err != nil {
		panic(err)
	}
	return req
}

func dummyRequestWithBodyNoGetBody(method string) *Request {
	req := dummyRequestWithBody(method)
	req.GetBody = nil
	return req
}

// issue22091Error acts like a golang.org/x/net/http2.ErrNoCachedConn.
type issue22091Error struct{}

func (issue22091Error) IsHTTP2NoCachedConnError() {}
func (issue22091Error) Error() string             { return "issue22091Error" }

func TestTransportShouldRetryRequest(t *testing.T) {
	tests := []struct {
		pc  *persistConn
		req *Request

		err  error
		want bool
	}{
		0: {
			pc:   &persistConn{reused: false},
			req:  dummyRequest("POST"),
			err:  nothingWrittenError{},
			want: false,
		},
		1: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("POST"),
			err:  nothingWrittenError{},
			want: true,
		},
		2: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("POST"),
			err:  http2ErrNoCachedConn,
			want: true,
		},
		3: {
			pc:   nil,
			req:  nil,
			err:  issue22091Error{}, // like an external http2ErrNoCachedConn
			want: true,
		},
		4: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("POST"),
			err:  errMissingHost,
			want: false,
		},
		5: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("POST"),
			err:  transportReadFromServerError{},
			want: false,
		},
		6: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("GET"),
			err:  transportReadFromServerError{},
			want: true,
		},
		7: {
			pc:   &persistConn{reused: true},
			req:  dummyRequest("GET"),
			err:  errServerClosedIdle,
			want: true,
		},
		8: {
			pc:   &persistConn{reused: true},
			req:  dummyRequestWithBody("POST"),
			err:  nothingWrittenError{},
			want: true,
		},
		9: {
			pc:   &persistConn{reused: true},
			req:  dummyRequestWithBodyNoGetBody("POST"),
			err:  nothingWrittenError{},
			want: false,
		},
	}
	for i, tt := range tests {
		got := tt.pc.shouldRetryRequest(tt.req, tt.err)
		if got != tt.want {
			t.Errorf("%d. shouldRetryRequest = %v; want %v", i, got, tt.want)
		}
	}
}

type roundTripFunc func(r *Request) (*Response, error)

func (f roundTripFunc) RoundTrip(r *Request) (*Response, error) {
	return f(r)
}

// Issue 25009
func TestTransportBodyAltRewind(t *testing.T) {
	cert, err := tls.X509KeyPair(internal.LocalhostCert, internal.LocalhostKey)
	if err != nil {
		t.Fatal(err)
	}
	ln := newLocalListener(t)
	defer ln.Close()

	go func() {
		tln := tls.NewListener(ln, &tls.Config{
			NextProtos:   []string{"foo"},
			Certificates: []tls.Certificate{cert},
		})
		for i := 0; i < 2; i++ {
			sc, err := tln.Accept()
			if err != nil {
				t.Error(err)
				return
			}
			if err := sc.(*tls.Conn).Handshake(); err != nil {
				t.Error(err)
				return
			}
			sc.Close()
		}
	}()

	addr := ln.Addr().String()
	req, _ := NewRequest("POST", "https://example.org/", bytes.NewBufferString("request"))
	roundTripped := false
	tr := &Transport{
		DisableKeepAlives: true,
		TLSNextProto: map[string]func(string, *tls.Conn) RoundTripper{
			"foo": func(authority string, c *tls.Conn) RoundTripper {
				return roundTripFunc(func(r *Request) (*Response, error) {
					n, _ := io.Copy(io.Discard, r.Body)
					if n == 0 {
						t.Error("body length is zero")
					}
					if roundTripped {
						return &Response{
							Body:       NoBody,
							StatusCode: 200,
						}, nil
					}
					roundTripped = true
					return nil, http2noCachedConnError{}
				})
			},
		},
		DialTLS: func(_, _ string) (net.Conn, error) {
			tc, err := tls.Dial("tcp", addr, &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{"foo"},
			})
			if err != nil {
				return nil, err
			}
			if err := tc.Handshake(); err != nil {
				return nil, err
			}
			return tc, nil
		},
	}
	c := &Client{Transport: tr}
	_, err = c.Do(req)
	if err != nil {
		t.Error(err)
	}
}

// Tests that gzipReader doesn't crash on a second Read call following
// the first Read call's gzip.NewReader returning an error.
func TestGzipReader_DoubleReadCrash(t *testing.T) {
	gz := &gzipReader{
		body: ioutil.NopCloser(strings.NewReader("0123456789")),
	}
	var buf [1]byte
	n, err1 := gz.Read(buf[:])
	if n != 0 || !strings.Contains(fmt.Sprint(err1), "invalid header") {
		t.Fatalf("Read = %v, %v; want 0, invalid header", n, err1)
	}
	n, err2 := gz.Read(buf[:])
	if n != 0 || err2 != err1 {
		t.Fatalf("second Read = %v, %v; want 0, %v", n, err2, err1)
	}
}

// gzipBody returns the gzip encoding of s.
func gzipBody(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readGzipBody decompresses one body through a gzipReader and closes it,
// which is what returns the underlying gzip.Reader to the pool.
func readGzipBody(t *testing.T, encoded []byte) string {
	t.Helper()
	gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(encoded))}
	got, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("ReadAll = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	return string(got)
}

// Tests that a gzip.Reader taken from the pool decompresses a new body
// correctly and carries no state from the body it read before.
func TestGzipReaderPoolReuse(t *testing.T) {
	bodies := []string{
		"first body",
		strings.Repeat("second body, long enough to span the window ", 2000),
		"",
		"third body",
	}
	for _, want := range bodies {
		if got := readGzipBody(t, gzipBody(t, want)); got != want {
			t.Fatalf("decompressed %d bytes, want %d bytes (mismatch)", len(got), len(want))
		}
	}
}

// Tests that a body abandoned before EOF does not recycle its reader (it
// may still be mid-stream) and does not corrupt later responses.
func TestGzipReaderAbandonedNotPooled(t *testing.T) {
	const first = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(gzipBody(t, first)))}
	var buf [4]byte
	if _, err := gz.Read(buf[:]); err != nil {
		t.Fatalf("Read = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if gz.zr == nil {
		t.Fatal("mid-stream reader was released; it must not be recycled")
	}

	const second = "a completely different body"
	if got := readGzipBody(t, gzipBody(t, second)); got != second {
		t.Fatalf("after abandoned body, got %q, want %q", got, second)
	}
}

// Tests that reads at EOF keep returning io.EOF after the reader has been
// recycled, and that Close stays idempotent.
func TestGzipReaderReadAfterEOF(t *testing.T) {
	gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(gzipBody(t, "body")))}
	if _, err := io.ReadAll(gz); err != nil {
		t.Fatalf("ReadAll = %v", err)
	}
	if gz.zr != nil {
		t.Fatal("gzip reader retained after EOF; it should have been recycled")
	}
	var buf [1]byte
	if n, err := gz.Read(buf[:]); n != 0 || err != io.EOF {
		t.Fatalf("Read after EOF = %v, %v; want 0, io.EOF", n, err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("first Close = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

// Tests that Close unblocking a concurrent Read (the bodyEOFSignal /
// cancelation pattern used by the transports) cannot hand the in-use gzip
// reader to another response. The blocking body never returns EOF, so the
// reader must never be recycled while Read is in flight.
func TestGzipReaderConcurrentCloseDoesNotRecycle(t *testing.T) {
	header := gzipBody(t, strings.Repeat("x", 1000))[:10] // valid header, stream never ends
	body := &blockingBody{
		Reader:  io.MultiReader(bytes.NewReader(header), neverEnding('x')),
		release: make(chan struct{}),
	}
	gz := &gzipReader{body: body}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var buf [512]byte
		for {
			if _, err := gz.Read(buf[:]); err != nil {
				return
			}
		}
	}()

	close(body.release) // let reads proceed briefly, then close concurrently
	if err := gz.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	body.stop()
	<-done
}

// blockingBody reads from Reader until stop is called, after which reads
// fail. release gates the first read so the test can order events.
type blockingBody struct {
	Reader  io.Reader
	release chan struct{}
	mu      sync.Mutex
	stopped bool
}

func (b *blockingBody) Read(p []byte) (int, error) {
	<-b.release
	b.mu.Lock()
	stopped := b.stopped
	b.mu.Unlock()
	if stopped {
		return 0, errors.New("body stopped")
	}
	return b.Reader.Read(p)
}

func (b *blockingBody) Close() error { return nil }

func (b *blockingBody) stop() {
	b.mu.Lock()
	b.stopped = true
	b.mu.Unlock()
}

type neverEnding byte

func (b neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// Tests that bodies which cannot be read do not poison or drain the pool.
// An empty body and a body that is not gzip at all both fail before any
// decompression happens; the reader they were given must stay usable and
// stay in the pool, and the failure must be sticky.
func TestGzipReaderUnreadableBodyKeepsPool(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{"invalid header", "not gzip at all"},
		{"empty body", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sentinel, err := gzip.NewReader(bytes.NewReader(gzipBody(t, "seed")))
			if err != nil {
				t.Fatal(err)
			}
			gzipReaderPool.Put(sentinel)

			gz := &gzipReader{body: ioutil.NopCloser(strings.NewReader(tt.body))}
			var buf [1]byte
			n, err1 := gz.Read(buf[:])
			if n != 0 || err1 == nil {
				t.Fatalf("Read = %v, %v; want 0 and an error", n, err1)
			}
			if gz.zr != nil {
				t.Fatal("reader retained after a failed header read")
			}
			if _, err2 := gz.Read(buf[:]); err2 != err1 {
				t.Fatalf("second Read = %v; want the sticky %v", err2, err1)
			}
			if err := gz.Close(); err != nil {
				t.Fatalf("Close = %v", err)
			}

			// The pool must still be able to serve a readable body.
			const want = "a valid body"
			if got := readGzipBody(t, gzipBody(t, want)); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

// Tests that a body which fails in Reset returns its reader to the pool
// instead of dropping it. An empty body fails that way, and dropping the
// reader would let a run of unreadable bodies empty the pool one reader at
// a time - each one taking a warmed reader out and discarding it.
//
// sync.Pool never promises that a Put value is visible to a later Get, so a
// single attempt can legitimately miss. Looping turns that into a reliable
// signal: when the reader is returned some attempt observes it, and when the
// reader is dropped no attempt can.
func TestGzipReaderUnreadableBodyReturnsReaderToPool(t *testing.T) {
	// Encoded up front so that nothing allocates between the Put and the
	// Read that should consume it.
	valid := gzipBody(t, "a valid body")
	seed := gzipBody(t, "seed")

	for i := 0; i < 50; i++ {
		sentinel, err := gzip.NewReader(bytes.NewReader(seed))
		if err != nil {
			t.Fatal(err)
		}
		gzipReaderPool.Put(sentinel)

		unreadable := &gzipReader{body: ioutil.NopCloser(strings.NewReader(""))}
		var buf [1]byte
		if _, err := unreadable.Read(buf[:]); err == nil {
			t.Fatal("Read of an empty body succeeded")
		}

		next := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(valid))}
		if _, err := next.Read(buf[:]); err != nil && err != io.EOF {
			t.Fatalf("Read = %v", err)
		}
		if next.zr == sentinel {
			return // the reader survived the unreadable body
		}
	}
	t.Fatal("an unreadable body never returned its reader to the pool")
}

func BenchmarkGzipReader(b *testing.B) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(strings.Repeat("hello world, this is a response body. ", 500))); err != nil {
		b.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		b.Fatal(err)
	}
	encoded := buf.Bytes()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(encoded))}
		if _, err := io.Copy(io.Discard, gz); err != nil {
			b.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// Tests that a reader carrying state from a previous body produces correct
// output when it is reused. The pool is seeded with a reader left part way
// through a different stream, which is the state a recycled reader is in
// after the body that used it was abandoned or after it decoded a body with
// entirely different contents.
func TestGzipReaderReuseScrubsPreviousState(t *testing.T) {
	dirty, err := gzip.NewReader(bytes.NewReader(gzipBody(t, strings.Repeat("stale ", 5000))))
	if err != nil {
		t.Fatal(err)
	}
	var scratch [16]byte
	if _, err := dirty.Read(scratch[:]); err != nil {
		t.Fatalf("priming read = %v", err)
	}
	gzipReaderPool.Put(dirty) // left mid-stream, with a populated window

	const want = "a completely unrelated body"
	for i := 0; i < 3; i++ {
		if got := readGzipBody(t, gzipBody(t, want)); got != want {
			t.Fatalf("pass %d: got %q, want %q", i, got, want)
		}
	}
}

// Tests a body made of concatenated gzip streams, which a gzip.Reader reads
// as one stream and only reports EOF at the end of the last one. A recycled
// reader must handle this the same way a fresh one does.
func TestGzipReaderMultistream(t *testing.T) {
	var encoded []byte
	encoded = append(encoded, gzipBody(t, "first stream ")...)
	encoded = append(encoded, gzipBody(t, "second stream ")...)
	encoded = append(encoded, gzipBody(t, "third stream")...)

	const want = "first stream second stream third stream"
	for i := 0; i < 3; i++ { // first pass allocates, later passes recycle
		if got := readGzipBody(t, encoded); got != want {
			t.Fatalf("pass %d: got %q, want %q", i, got, want)
		}
	}
}

// Tests that concurrent responses sharing the pool never receive each
// other's data. Run under -race this also covers the reader being handed to
// two goroutines at once.
func TestGzipReaderPoolConcurrent(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 25

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				// Distinct payload per body, so any cross-contamination
				// between pooled readers shows up as a mismatch.
				want := fmt.Sprintf("goroutine %d body %d ", g, i) +
					strings.Repeat(fmt.Sprintf("%d", g), 100*(i+1))
				gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(gzipBody(t, want)))}
				got, err := io.ReadAll(gz)
				if err != nil {
					t.Errorf("goroutine %d body %d: ReadAll = %v", g, i, err)
					return
				}
				if err := gz.Close(); err != nil {
					t.Errorf("goroutine %d body %d: Close = %v", g, i, err)
					return
				}
				if string(got) != want {
					t.Errorf("goroutine %d body %d: decompressed content does not match its own payload", g, i)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// Tests that a truncated body, whose stream ends without the gzip trailer,
// is reported as an unexpected EOF and does not recycle its reader.
func TestGzipReaderTruncatedNotRecycled(t *testing.T) {
	encoded := gzipBody(t, strings.Repeat("payload ", 100))
	truncated := encoded[:len(encoded)-6] // drop part of the CRC/size trailer

	gz := &gzipReader{body: ioutil.NopCloser(bytes.NewReader(truncated))}
	if _, err := io.ReadAll(gz); err == nil {
		t.Fatal("ReadAll of a truncated gzip body succeeded")
	} else if err == io.EOF {
		t.Fatalf("truncated body reported clean EOF: %v", err)
	}
	if gz.zr == nil {
		t.Fatal("reader of a truncated body was recycled")
	}
}
