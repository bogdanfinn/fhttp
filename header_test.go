// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bytes"
	"io"
	"math/rand"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bogdanfinn/fhttp/internal/race"
)

var headerWriteTests = []struct {
	h        Header
	exclude  map[string]bool
	expected string
}{
	{Header{}, nil, ""},
	{
		Header{
			"Content-Type":   {"text/html; charset=UTF-8"},
			"Content-Length": {"0"},
		},
		nil,
		"Content-Length: 0\r\nContent-Type: text/html; charset=UTF-8\r\n",
	},
	{
		Header{
			"Content-Length": {"0", "1", "2"},
		},
		nil,
		"Content-Length: 0\r\nContent-Length: 1\r\nContent-Length: 2\r\n",
	},
	{
		Header{
			"Expires":          {"-1"},
			"Content-Length":   {"0"},
			"Content-Encoding": {"gzip"},
		},
		map[string]bool{"Content-Length": true},
		"Content-Encoding: gzip\r\nExpires: -1\r\n",
	},
	{
		Header{
			"Expires":          {"-1"},
			"Content-Length":   {"0", "1", "2"},
			"Content-Encoding": {"gzip"},
		},
		map[string]bool{"Content-Length": true},
		"Content-Encoding: gzip\r\nExpires: -1\r\n",
	},
	{
		Header{
			"Expires":          {"-1"},
			"Content-Length":   {"0"},
			"Content-Encoding": {"gzip"},
		},
		map[string]bool{"Content-Length": true, "Expires": true, "Content-Encoding": true},
		"",
	},
	{
		Header{
			"Nil":          nil,
			"Empty":        {},
			"Blank":        {""},
			"Double-Blank": {"", ""},
		},
		nil,
		"Blank: \r\nDouble-Blank: \r\nDouble-Blank: \r\n",
	},
	// Tests header sorting when over the insertion sort threshold side:
	{
		Header{
			"k1": {"1a", "1b"},
			"k2": {"2a", "2b"},
			"k3": {"3a", "3b"},
			"k4": {"4a", "4b"},
			"k5": {"5a", "5b"},
			"k6": {"6a", "6b"},
			"k7": {"7a", "7b"},
			"k8": {"8a", "8b"},
			"k9": {"9a", "9b"},
		},
		map[string]bool{"k5": true},
		"k1: 1a\r\nk1: 1b\r\nk2: 2a\r\nk2: 2b\r\nk3: 3a\r\nk3: 3b\r\n" +
			"k4: 4a\r\nk4: 4b\r\nk6: 6a\r\nk6: 6b\r\n" +
			"k7: 7a\r\nk7: 7b\r\nk8: 8a\r\nk8: 8b\r\nk9: 9a\r\nk9: 9b\r\n",
	},
	// Test sorting headers by the special Header-Order header
	{
		Header{
			"a":            {"2"},
			"b":            {"3"},
			"e":            {"1"},
			"c":            {"5"},
			"d":            {"4"},
			HeaderOrderKey: {"e", "a", "b", "d", "c"},
		},
		nil,
		"e: 1\r\na: 2\r\nb: 3\r\nd: 4\r\nc: 5\r\n",
	},
	// Make sure that http 1.1 capitla letters are also sorted properly
	{
		Header{
			"X-NewRelic-ID":         {"12345"},
			"x-api-key":             {"ABCDEFGHIJKLMNOPQRSTUVWXYZ"},
			"MESH-Commerce-Channel": {"android-app-phone"},
			"mesh-version":          {"cart=4"},
			"User-Agent":            {"size/3.1.0.8355 (android-app-phone; Android 10; Build/CPH2185_11_A.28)"},
			"X-Request-Auth":        {"hawkHeader"},
			"X-acf-sensor-data":     {"3456"},
			"Content-Type":          {"application/json; charset=UTF-8"},
			"Accept":                {"application/json"},
			"Transfer-Encoding":     {"chunked"},
			"Host":                  {"prod.jdgroupmesh.cloud"},
			"Connection":            {"Keep-Alive"},
			"Accept-Encoding":       {"gzip"},
			// The header order slice must be lowercase, even though the
			// header keys themselves are capitalized (see README).
			HeaderOrderKey: {
				"x-newrelic-id",
				"x-api-key",
				"mesh-commerce-channel",
				"mesh-version",
				"user-agent",
				"x-request-auth",
				"x-acf-sensor-data",
				"content-type",
				"accept",
				"transfer-encoding",
				"host",
				"connection",
				"accept-encoding",
			},
		},
		nil,
		"X-NewRelic-ID: 12345\r\nx-api-key: ABCDEFGHIJKLMNOPQRSTUVWXYZ\r\nMESH-Commerce-Channel: android-app-phone\r\n" +
			"mesh-version: cart=4\r\nUser-Agent: size/3.1.0.8355 (android-app-phone; Android 10; Build/CPH2185_11_A.28)\r\n" +
			"X-Request-Auth: hawkHeader\r\nX-acf-sensor-data: 3456\r\nContent-Type: application/json; charset=UTF-8\r\n" +
			"Accept: application/json\r\nTransfer-Encoding: chunked\r\nHost: prod.jdgroupmesh.cloud\r\nConnection: Keep-Alive\r\n" +
			"Accept-Encoding: gzip\r\n",
	},
}

func TestHeaderWrite(t *testing.T) {
	var buf bytes.Buffer
	for i, test := range headerWriteTests {
		test.h.WriteSubset(&buf, test.exclude)
		if buf.String() != test.expected {
			t.Errorf("#%d:\n got: %q\nwant: %q", i, buf.String(), test.expected)
		}
		buf.Reset()
	}
}

var parseTimeTests = []struct {
	h   Header
	err bool
}{
	{Header{"Date": {""}}, true},
	{Header{"Date": {"invalid"}}, true},
	{Header{"Date": {"1994-11-06T08:49:37Z00:00"}}, true},
	{Header{"Date": {"Sun, 06 Nov 1994 08:49:37 GMT"}}, false},
	{Header{"Date": {"Sunday, 06-Nov-94 08:49:37 GMT"}}, false},
	{Header{"Date": {"Sun Nov  6 08:49:37 1994"}}, false},
}

func TestParseTime(t *testing.T) {
	expect := time.Date(1994, 11, 6, 8, 49, 37, 0, time.UTC)
	for i, test := range parseTimeTests {
		d, err := ParseTime(test.h.Get("Date"))
		if err != nil {
			if !test.err {
				t.Errorf("#%d:\n got err: %v", i, err)
			}
			continue
		}
		if test.err {
			t.Errorf("#%d:\n  should err", i)
			continue
		}
		if !expect.Equal(d) {
			t.Errorf("#%d:\n got: %v\nwant: %v", i, d, expect)
		}
	}
}

type hasTokenTest struct {
	header string
	token  string
	want   bool
}

var hasTokenTests = []hasTokenTest{
	{"", "", false},
	{"", "foo", false},
	{"foo", "foo", true},
	{"foo ", "foo", true},
	{" foo", "foo", true},
	{" foo ", "foo", true},
	{"foo,bar", "foo", true},
	{"bar,foo", "foo", true},
	{"bar, foo", "foo", true},
	{"bar,foo, baz", "foo", true},
	{"bar, foo,baz", "foo", true},
	{"bar,foo, baz", "foo", true},
	{"bar, foo, baz", "foo", true},
	{"FOO", "foo", true},
	{"FOO ", "foo", true},
	{" FOO", "foo", true},
	{" FOO ", "foo", true},
	{"FOO,BAR", "foo", true},
	{"BAR,FOO", "foo", true},
	{"BAR, FOO", "foo", true},
	{"BAR,FOO, baz", "foo", true},
	{"BAR, FOO,BAZ", "foo", true},
	{"BAR,FOO, BAZ", "foo", true},
	{"BAR, FOO, BAZ", "foo", true},
	{"foobar", "foo", false},
	{"barfoo ", "foo", false},
}

func TestHasToken(t *testing.T) {
	for _, tt := range hasTokenTests {
		if hasToken(tt.header, tt.token) != tt.want {
			t.Errorf("hasToken(%q, %q) = %v; want %v", tt.header, tt.token, !tt.want, tt.want)
		}
	}
}

func TestNilHeaderClone(t *testing.T) {
	t1 := Header(nil)
	t2 := t1.Clone()
	if t2 != nil {
		t.Errorf("cloned header does not match original: got: %+v; want: %+v", t2, nil)
	}
}

var testHeader = Header{
	"Content-Length": {"123"},
	"Content-Type":   {"text/plain"},
	"Date":           {"some date at some time Z"},
	"Server":         {DefaultUserAgent},
}

var buf bytes.Buffer

func BenchmarkHeaderWriteSubset(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		testHeader.WriteSubset(&buf, nil)
	}
}

func TestHeaderWriteSubsetAllocs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping alloc test in short mode")
	}
	if race.Enabled {
		t.Skip("skipping test under race detector")
	}
	if runtime.GOMAXPROCS(0) > 1 {
		t.Skip("skipping; GOMAXPROCS>1")
	}
	n := testing.AllocsPerRun(100, func() {
		buf.Reset()
		testHeader.WriteSubset(&buf, nil)
	})
	if n > 0 {
		t.Errorf("allocs = %g; want 0", n)
	}
}

// Issue 34878: test that every call to
// cloneOrMakeHeader never returns a nil Header.
func TestCloneOrMakeHeader(t *testing.T) {
	tests := []struct {
		name     string
		in, want Header
	}{
		{"nil", nil, Header{}},
		{"empty", Header{}, Header{}},
		{
			name: "non-empty",
			in:   Header{"foo": {"bar"}},
			want: Header{"foo": {"bar"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cloneOrMakeHeader(tt.in)
			if got == nil {
				t.Fatal("unexpected nil Header")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Got:  %#v\nWant: %#v", got, tt.want)
			}
			got.Add("A", "B")
			got.Get("A")
		})
	}
}

// TestHTTP1HeaderOrder tests capitalized http1.1 header order written by request
func TestHTTP1HeaderOrder(t *testing.T) {
	req, err := NewRequest("GET", "https://prod.jdgroupmesh.cloud/stores/size/products/16069871?channel=android-app-phone&expand=variations,informationBlocks,customisations", nil)
	if err != nil {
		t.Fatal(err.Error())
	}
	req.Header = Header{
		"X-NewRelic-ID":         {"12345"},
		"x-api-key":             {"ABCDE12345"},
		"MESH-Commerce-Channel": {"android-app-phone"},
		"mesh-version":          {"cart=4"},
		"User-Agent":            {"size/3.1.0.8355 (android-app-phone; Android 10; Build/CPH2185_11_A.28)"},
		"X-Request-Auth":        {"hawkHeader"},
		"X-acf-sensor-data":     {"3456"},
		"Content-Type":          {"application/json; charset=UTF-8"},
		"Accept":                {"application/json"},
		"Transfer-Encoding":     {"chunked"},
		"Host":                  {"prod.jdgroupmesh.cloud"},
		"Connection":            {"Keep-Alive"},
		"Accept-Encoding":       {"gzip"},
		HeaderOrderKey: {
			"x-newrelic-id",
			"x-api-key",
			"mesh-commerce-channel",
			"mesh-version",
			"user-agent",
			"x-request-auth",
			"x-acf-sensor-data",
			"transfer-encoding",
			"content-type",
			"accept",
			"host",
			"connection",
			"accept-encoding",
		},
		PHeaderOrderKey: {
			":method",
			":path",
			":authority",
			":scheme",
		},
	}

	var b []byte
	buf := bytes.NewBuffer(b)
	err = req.Write(buf)
	if err != nil {
		t.Fatal(err.Error())
	}
	expected := "GET /stores/size/products/16069871?channel=android-app-phone&expand=variations,informationBlocks,customisations HTTP/1.1\r\nX-NewRelic-ID: 12345\r\nx-api-key: ABCDE12345\r\nMESH-Commerce-Channel: android-app-phone\r\nmesh-version: cart=4\r\nUser-Agent: size/3.1.0.8355 (android-app-phone; Android 10; Build/CPH2185_11_A.28)\r\nX-Request-Auth: hawkHeader\r\nX-acf-sensor-data: 3456\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json; charset=UTF-8\r\nAccept: application/json\r\nHost: prod.jdgroupmesh.cloud\r\nConnection: Keep-Alive\r\nAccept-Encoding: gzip\r\n\r\n"
	if expected != buf.String() {
		t.Fatalf("got:\n%swant:\n%s", buf.String(), expected)
	}
}

func TestWriteSubsetDoesNotMutateExclude(t *testing.T) {
	h := Header{
		"Keep-One":      {"1"},
		"Drop-Me":       {"nope"},
		"Keep-Two":      {"2"},
		HeaderOrderKey:  {"keep-two", "keep-one"},
		PHeaderOrderKey: {":method"},
	}
	exclude := map[string]bool{"Drop-Me": true}

	var buf bytes.Buffer
	if err := h.WriteSubset(&buf, exclude); err != nil {
		t.Fatal(err)
	}

	if want := map[string]bool{"Drop-Me": true}; !reflect.DeepEqual(exclude, want) {
		t.Errorf("WriteSubset mutated the caller's exclude map: got %v, want %v", exclude, want)
	}
	got := buf.String()
	if want := "Keep-Two: 2\r\nKeep-One: 1\r\n"; got != want {
		t.Errorf("WriteSubset output = %q, want %q", got, want)
	}
}

func TestWriteSubsetSharedExcludeConcurrent(t *testing.T) {
	// Callers such as Response.Write pass a shared package-level exclude
	// map. Concurrent writes with and without a Header-Order: key must
	// not race on it (this test is only meaningful under -race).
	shared := map[string]bool{"Content-Length": true}
	ordered := Header{
		"B-Second":     {"2"},
		"A-First":      {"1"},
		HeaderOrderKey: {"a-first", "b-second"},
	}
	plain := Header{
		"Zulu":  {"1"},
		"Alpha": {"2"},
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		h := ordered
		if i%2 == 1 {
			h = plain
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if err := h.WriteSubset(io.Discard, shared); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkHeaderWriteSubsetParallel(b *testing.B) {
	b.ReportAllocs()
	h := Header{
		"sec-ch-ua":       {"\"Chromium\";v=\"124\""},
		"accept":          {"*/*"},
		"user-agent":      {"Mozilla/5.0"},
		"content-type":    {"application/json"},
		"accept-language": {"en-US,en;q=0.9"},
		"accept-encoding": {"gzip, deflate, br"},
		"referer":         {"https://example.org/x"},
		HeaderOrderKey: {
			"sec-ch-ua", "accept", "user-agent", "content-type",
			"referer", "accept-encoding", "accept-language",
		},
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := h.WriteSubset(io.Discard, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestHeaderSorterPoolReuse(t *testing.T) {
	// A sorter used by SortedKeyValuesBy keeps its order map. When it is
	// pulled from the pool again by SortedKeyValues (no order), the stale
	// order must not be used.
	ordered := Header{
		"Zebra": {"1"},
		"Apple": {"2"},
	}
	// Deliberately the reverse of lexicographic order.
	_, hs := ordered.SortedKeyValuesBy(map[string]int{"zebra": 0, "apple": 1}, nil)
	headerSorterPool.Put(hs)

	plain := Header{
		"Apple":  {"1"},
		"Banana": {"2"},
		"Zebra":  {"3"},
	}
	kvs, _ := plain.SortedKeyValues(nil)
	for i := 1; i < len(kvs); i++ {
		if kvs[i-1].Key > kvs[i].Key {
			t.Fatalf("keys not sorted lexicographically: %q before %q", kvs[i-1].Key, kvs[i].Key)
		}
	}
}

func TestSortedKeyValuesBy(t *testing.T) {
	tests := []struct {
		name  string
		h     Header
		order map[string]int
		want  []string
	}{
		{
			name: "all keys in order",
			h: Header{
				"Accept":     {"*/*"},
				"User-Agent": {"x"},
				"Referer":    {"y"},
			},
			order: map[string]int{"user-agent": 0, "referer": 1, "accept": 2},
			want:  []string{"User-Agent", "Referer", "Accept"},
		},
		{
			name: "keys absent from order sort lexicographically after ordered keys",
			h: Header{
				"Zeta":       {"z"},
				"Alpha":      {"a"},
				"Mid":        {"m"},
				"In-Order":   {"x"},
				"Also-Order": {"y"},
			},
			order: map[string]int{"in-order": 0, "also-order": 1},
			want:  []string{"In-Order", "Also-Order", "Alpha", "Mid", "Zeta"},
		},
		{
			name: "order lookup lowercases header keys",
			h: Header{
				"CONTENT-TYPE": {"a"},
				"Accept":       {"b"},
			},
			order: map[string]int{"content-type": 0, "accept": 1},
			want:  []string{"CONTENT-TYPE", "Accept"},
		},
		{
			name: "no keys in order is fully lexicographic",
			h: Header{
				"B": {"1"},
				"A": {"2"},
				"C": {"3"},
			},
			order: map[string]int{"unrelated": 0},
			want:  []string{"A", "B", "C"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kvs, hs := tt.h.SortedKeyValuesBy(tt.order, nil)
			got := make([]string, 0, len(kvs))
			for _, kv := range kvs {
				got = append(got, kv.Key)
			}
			headerSorterPool.Put(hs)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SortedKeyValuesBy(%v) key order = %v, want %v", tt.order, got, tt.want)
			}
		})
	}
}

func TestSortedKeyValuesByPoolReuse(t *testing.T) {
	// Reuse one pooled sorter across ordered sorts of different sizes,
	// with an orderless sort in between; per-sort state must be resized
	// and repopulated on every call.
	sortKeys := func(h Header, order map[string]int) []string {
		var kvs []HeaderKeyValues
		var hs *headerSorter
		if order != nil {
			kvs, hs = h.SortedKeyValuesBy(order, nil)
		} else {
			kvs, hs = h.SortedKeyValues(nil)
		}
		got := make([]string, 0, len(kvs))
		for _, kv := range kvs {
			got = append(got, kv.Key)
		}
		headerSorterPool.Put(hs)
		return got
	}

	big := Header{"A": {"1"}, "B": {"2"}, "C": {"3"}, "D": {"4"}, "E": {"5"}}
	bigOrder := map[string]int{"e": 0, "d": 1, "c": 2, "b": 3, "a": 4}
	if got, want := sortKeys(big, bigOrder), []string{"E", "D", "C", "B", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("big ordered sort = %v, want %v", got, want)
	}

	small := Header{"Y": {"1"}, "X": {"2"}}
	if got, want := sortKeys(small, map[string]int{"y": 0, "x": 1}), []string{"Y", "X"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("small ordered sort after big = %v, want %v", got, want)
	}

	if got, want := sortKeys(big, nil), []string{"A", "B", "C", "D", "E"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("orderless sort after ordered = %v, want %v", got, want)
	}

	if got, want := sortKeys(big, bigOrder), []string{"E", "D", "C", "B", "A"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered sort after orderless = %v, want %v", got, want)
	}
}

func TestSortedKeyValuesByDuplicateOrderValues(t *testing.T) {
	// A Header-Order: list with a repeated entry produces an order map
	// whose values can reach or exceed len(order), e.g. ["c","a","c"]
	// gives {"c": 2, "a": 1}. Keys present in the order map must still
	// sort ahead of absent keys.
	h := Header{
		"Charlie": {"1"},
		"Alpha":   {"2"},
		"Mango":   {"3"}, // absent from order
	}
	order := map[string]int{"charlie": 2, "alpha": 1}
	kvs, hs := h.SortedKeyValuesBy(order, nil)
	got := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		got = append(got, kv.Key)
	}
	headerSorterPool.Put(hs)
	want := []string{"Alpha", "Charlie", "Mango"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedKeyValuesBy(%v) key order = %v, want %v", order, got, want)
	}
}

// TestHeaderSorterLessEquivalence checks the cached-lookup Less against a
// reference implementation of the original per-comparison semantics across
// adversarial order maps: duplicate values, values at or past len(order),
// negative values, and keys that collide when lowercased.
func TestHeaderSorterLessEquivalence(t *testing.T) {
	referenceLess := func(kvs []HeaderKeyValues, order map[string]int, i, j int) bool {
		idxi, iok := order[strings.ToLower(kvs[i].Key)]
		idxj, jok := order[strings.ToLower(kvs[j].Key)]
		if !iok && !jok {
			return kvs[i].Key < kvs[j].Key
		} else if !iok && jok {
			return false
		} else if iok && !jok {
			return true
		}
		return idxi < idxj
	}

	rng := rand.New(rand.NewSource(1))
	keyPool := []string{
		"Accept", "accept", "ACCEPT", "User-Agent", "user-agent",
		"Cookie", "Referer", "X-A", "x-a", "Zeta", "alpha", "Alpha",
	}
	for iter := 0; iter < 200; iter++ {
		n := 2 + rng.Intn(len(keyPool)-2)
		keys := make([]string, n)
		perm := rng.Perm(len(keyPool))
		for i := range keys {
			keys[i] = keyPool[perm[i]]
		}

		order := make(map[string]int)
		for _, k := range keys {
			if rng.Intn(2) == 0 {
				order[strings.ToLower(k)] = rng.Intn(n+3) - 2 // gaps, duplicates, negatives
			}
		}

		kvs := make([]HeaderKeyValues, n)
		for i, k := range keys {
			kvs[i] = HeaderKeyValues{Key: k, Values: []string{"v"}}
		}

		hs := &headerSorter{kvs: kvs, order: order}
		hs.orderIdx = make([]int, n)
		hs.orderOK = make([]bool, n)
		for i, kv := range kvs {
			hs.orderIdx[i], hs.orderOK[i] = order[strings.ToLower(kv.Key)]
		}

		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if got, want := hs.Less(i, j), referenceLess(kvs, order, i, j); got != want {
					t.Fatalf("iter %d: Less(%q, %q) with order %v = %v, want %v",
						iter, kvs[i].Key, kvs[j].Key, order, got, want)
				}
			}
		}
	}
}
