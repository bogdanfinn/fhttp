package http

import "testing"

// looksLikeZlib has to agree with compress/zlib about what a zlib header is.
//
// The header this replaced tested the first byte against 0x78 and the second
// against the four values a default-window compressor produces. Those are the
// common cases of RFC 1950's rule, not the rule, and the gap is not academic:
// 0x68 0x05 is a 16K-window stream that compress/zlib reads and that check
// refuses, so it would have been decoded as raw deflate.
func TestLooksLikeZlibMatchesTheRFC(t *testing.T) {
	tests := []struct {
		name   string
		header [2]byte
		want   bool
	}{
		{"default window, level default", [2]byte{0x78, 0x9C}, true},
		{"default window, level best", [2]byte{0x78, 0xDA}, true},
		{"default window, level low", [2]byte{0x78, 0x01}, true},
		{"default window, level medium", [2]byte{0x78, 0x5E}, true},
		{"16K window", [2]byte{0x68, 0x05}, true},
		{"smallest window", [2]byte{0x08, 0x1D}, true},

		{"not a multiple of 31", [2]byte{0x78, 0x9D}, false},
		{"window out of range", [2]byte{0x88, 0x1D}, false},
		{"method is not deflate", [2]byte{0x79, 0x01}, false},
		{"raw deflate, final stored block", [2]byte{0x01, 0x00}, false},
		{"raw deflate, fixed Huffman", [2]byte{0x4B, 0x4C}, false},
	}

	for _, tt := range tests {
		if got := looksLikeZlib(tt.header); got != tt.want {
			t.Errorf("%s: looksLikeZlib(%02x %02x) = %v, want %v",
				tt.name, tt.header[0], tt.header[1], got, tt.want)
		}
	}
}
