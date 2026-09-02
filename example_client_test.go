package http_test

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	tls "github.com/bogdanfinn/utls"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/http2"
	"github.com/bogdanfinn/fhttp/httptest"
)

// Basic http test with Header Order + enable push
func TestExample(t *testing.T) {
	c := http.Client{}
	// Otherwise the connection outlives the test and the goroutine-leak
	// check in TestMain/afterTest flags every test that runs afterwards.
	defer c.CloseIdleConnections()

	req, err := http.NewRequest("GET", "https://httpbin.org/headers", strings.NewReader(""))

	if err != nil {
		t.Error(err)
		return
	}

	req.Header = http.Header{
		"sec-ch-ua":                 {"\" Not A;Brand\";v=\"99\", \"Chromium\";v=\"90\", \"Google Chrome\";v=\"90\""},
		"sec-ch-ua-mobile":          {"?0"},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.93 Safari/537.36"},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-user":            {"?1"},
		"sec-fetch-dest":            {"document"},
		"accept-encoding":           {"gzip, deflate, br"},
		http.HeaderOrderKey: {
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"upgrade-insecure-requests",
			"user-agent",
			"accept",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-user",
			"sec-fetch-dest",
			"accept-encoding",
		},
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Error(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("Expected status code 200, got %v", resp.StatusCode)
	}

	var data interface{}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		t.Error(err)
	}
}

func getCharlesCert() (*x509.CertPool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	caCert, err := os.ReadFile(fmt.Sprintf("%v/charles_cert.pem", home))
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	certPool.AppendCertsFromPEM(caCert)
	return certPool, nil
}

func addCharlesToTransport(tr *http.Transport, proxy string) error {
	caCertPool, err := getCharlesCert()
	if err != nil {
		return err
	}
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return err
	}
	tr.TLSClientConfig = &tls.Config{
		RootCAs: caCertPool,
	}
	tr.Proxy = http.ProxyURL(proxyURL)

	return nil
}

func addWiresharkToTransport(tr *http.Transport) error {
	kl := flag.String("keylog", "ssl-keylog.txt", "file to dump ssl keys")
	keylog, err := os.OpenFile(*kl, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	tr.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
		KeyLogWriter:       keylog,
	}
	return nil
}

// Test with Charles cert + proxy
func TestWithCert(t *testing.T) {
	h1t := &http.Transport{
		ForceAttemptHTTP2: true,
	}
	defer h1t.CloseIdleConnections()
	if err := addCharlesToTransport(h1t, "http://localhost:8888"); err != nil {
		// Requires a local Charles proxy and its CA cert in $HOME.
		t.Skipf("Charles proxy not configured locally: %v", err)
	}

	t2, err := http2.ConfigureTransports(h1t)
	if err != nil {
		t.Fatal(err)
	}
	t2.Settings = map[http2.SettingID]uint32{
		http2.SettingMaxConcurrentStreams: 1000,
		http2.SettingMaxFrameSize:         16384,
		http2.SettingMaxHeaderListSize:    262144,
	}
	t2.InitialWindowSize = 6291456
	t2.HeaderTableSize = 65536
	h1t.H2transport = t2

	client := http.Client{
		Transport: h1t,
	}

	req, err := http.NewRequest("GET", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Encoding", nil)
	if err != nil {
		t.Error(err)
		return
	}

	req.Header = http.Header{
		"sec-ch-ua":                 {"\" Not A;Brand\";v=\"99\", \"Chromium\";v=\"90\", \"Google Chrome\";v=\"90\""},
		"sec-ch-ua-mobile":          {"?0"},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.93 Safari/537.36", "I shouldn't be here"},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-user":            {"?1"},
		"cookie":                    {"cf_clearance=67f509a97bae8bb8349523a14c0ca3d7d8460c93-1620778862-0-250", "wp_customerGroup=NOT+LOGGED+IN"},
		"sec-fetch-dest":            {"document"},
		"accept-encoding":           {"gzip, deflate, br"},
		"not-included-header":       {"should be last"},
		http.HeaderOrderKey: {
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"upgrade-insecure-requests",
			"user-agent",
			"cookie",
			"accept",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-user",
			"sec-fetch-dest",
			"accept-encoding",
		},
		http.PHeaderOrderKey: {":method", ":authority", ":scheme", ":path"},
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Error(err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status code 200, got %v", resp.StatusCode)
	}
}

// Test with push handler
func TestEnablePush(t *testing.T) {
	t1 := &http.Transport{
		ForceAttemptHTTP2: true,
	}
	defer t1.CloseIdleConnections()
	t2, err := http2.ConfigureTransports(t1)
	if err != nil {
		t.Fatal(err)
	}
	t2.PushHandler = &http2.DefaultPushHandler{}
	t1.H2transport = t2
	c := &http.Client{
		Transport: t1,
	}
	var req *http.Request
	req, err = http.NewRequest("GET", "https://httpbin.org/headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, err = http.NewRequest("POST", "https://httpbin.org/post", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// Test finishline
func TestFinishLine(t *testing.T) {
	t1 := &http.Transport{
		ForceAttemptHTTP2: true,
	}
	defer t1.CloseIdleConnections()

	if err := addCharlesToTransport(t1, "http://localhost:8888"); err != nil {
		// Requires a local Charles proxy and its CA cert in $HOME.
		t.Skipf("Charles proxy not configured locally: %v", err)
	}
	// if err := addWiresharkToTransport(t1); err != nil {
	// 	t.Fatal(err)
	// }
	t2, err := http2.ConfigureTransports(t1)
	if err != nil {
		t.Fatal(err)
	}
	t2.Settings = map[http2.SettingID]uint32{
		http2.SettingMaxConcurrentStreams: 1000,
		http2.SettingMaxFrameSize:         16384,
		http2.SettingMaxHeaderListSize:    262144,
	}
	t2.InitialWindowSize = 6291456
	t2.HeaderTableSize = 65536
	t2.PushHandler = &http2.DefaultPushHandler{}
	t1.H2transport = t2

	c := &http.Client{
		Transport: t1,
	}
	req, err := http.NewRequest("GET", "https://www.finishline.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{
		"sec-ch-ua":                 {"\" Not A;Brand\";v=\"99\", \"Chromium\";v=\"90\", \"Google Chrome\";v=\"90\""},
		"sec-ch-ua-mobile":          {"?0"},
		"upgrade-insecure-requests": {"1"},
		"user-agent":                {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.93 Safari/537.36"},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9"},
		"sec-fetch-site":            {"none"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-user":            {"?1"},
		"sec-fetch-dest":            {"document"},
		"accept-encoding":           {"gzip, deflate, br"},
		http.HeaderOrderKey: {
			"sec-ch-ua",
			"sec-ch-ua-mobile",
			"upgrade-insecure-requests",
			"user-agent",
			"accept",
			"sec-fetch-site",
			"sec-fetch-mode",
			"sec-fetch-user",
			"sec-fetch-dest",
			"accept-encoding",
		},
		http.PHeaderOrderKey: {":method", ":authority", ":scheme", ":path"},
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Got status %v from finishline, expected 200", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("resp: %v\n", string(b)[1])
}

// Test compression brotli
func TestCompressionBrotli(t *testing.T) {
	t1 := &http.Transport{
		ForceAttemptHTTP2: true,
	}
	defer t1.CloseIdleConnections()
	c := http.Client{
		Transport: t1,
	}
	req, _ := http.NewRequest("GET", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Encoding", nil)
	req.Header = http.Header{
		"accept-encoding": {"br"},
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if h := resp.Header.Get("content-encoding"); h == "" || h != "br" {
		t.Fatalf("Got content-encoding header %v, expected br", h)
	}
}

const deflateBody = "Content-Encoding: deflate covers both zlib-wrapped and raw deflate streams."

// deflateServer serves deflateBody as Content-Encoding: deflate, either
// zlib-wrapped (RFC 1950) or as a raw deflate stream (RFC 1951).
func deflateServer(t *testing.T, zlibWrapped bool) *httptest.Server {
	t.Helper()

	var buf bytes.Buffer
	var w io.WriteCloser
	if zlibWrapped {
		w = zlib.NewWriter(&buf)
	} else {
		fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	}
	if _, err := io.WriteString(w, deflateBody); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	payload := buf.Bytes()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "deflate")
		w.Write(payload)
	}))
}

// A caller that sets accept-encoding itself owns the decoding: the transport
// must not decompress the body or strip Content-Encoding. Verify that, and
// that the library decodes both deflate flavours.
func testCompressionDeflate(t *testing.T, zlibWrapped bool) {
	t.Helper()

	ts := deflateServer(t, zlibWrapped)
	defer ts.Close()

	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{
		"accept-encoding": {"deflate"},
	}

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if h := resp.Header.Get("content-encoding"); h != "deflate" {
		t.Fatalf("Expected content encoding deflate, got %q", h)
	}

	got, err := io.ReadAll(http.DecompressBodyByType(resp.Body, "deflate"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != deflateBody {
		t.Errorf("body = %q; want %q", got, deflateBody)
	}
}

// Test compression zlib deflate
func TestCompressionZlibDeflate(t *testing.T) {
	testCompressionDeflate(t, true)
}

// Test compression deflate
//
// NOTE: this currently fails. identifyDeflate only recognises zlib-wrapped
// streams (first byte 0x78) and has no raw-deflate fallback, and on that
// fallback path it returns the body after having already consumed two bytes,
// so the payload comes back both undecoded and truncated by two bytes.
func TestCompressionDeflate(t *testing.T) {
	testCompressionDeflate(t, false)
}

// Test with cookies
// Test with missing in header order, that should be added
// Test for UA that has empty string, excluding UA from being part of headers
