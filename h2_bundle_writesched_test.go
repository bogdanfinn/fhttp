//go:build !nethttpomithttp2

package http

import "testing"

// RST_STREAM frames must not queue behind a stream's blocked frames
// (golang.org/issue/49741). This mirrors TestRandomScheduler in package http2.
func TestBundledRandomSchedulerRSTStreamFirst(t *testing.T) {
	ws := http2NewRandomWriteScheduler()
	st := &http2stream{id: 1}
	ws.Push(http2FrameWriteRequest{write: &http2writeResHeaders{streamID: 1, httpResCode: 200}, stream: st})
	ws.Push(http2FrameWriteRequest{write: http2streamError(1, http2ErrCodeCancel)})

	wr, ok := ws.Pop()
	if !ok {
		t.Fatal("Pop returned no frame")
	}
	if _, ok := wr.write.(http2StreamError); !ok {
		t.Fatalf("first frame = %v, want RST_STREAM", wr)
	}
}
