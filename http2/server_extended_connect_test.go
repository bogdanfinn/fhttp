package http2

import (
	"os"
	"os/exec"
	"testing"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// Extended CONNECT is off unless GODEBUG=http2xconnect=1 at startup
// (golang/go#71128). GODEBUG is read at init, so each setting runs in a new
// process.
func TestServerExtendedConnect(t *testing.T) {
	if want := os.Getenv("FHTTP_TEST_XCONNECT"); want != "" {
		testServerExtendedConnect(t, want == "on")
		return
	}
	for _, tt := range []struct{ godebug, want string }{
		{"", "off"},
		{"http2xconnect=0", "off"},
		{"http2xconnect=1", "on"},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestServerExtendedConnect$")
		cmd.Env = append(os.Environ(), "FHTTP_TEST_XCONNECT="+tt.want, "GODEBUG="+tt.godebug)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("GODEBUG=%q: %v\n%s", tt.godebug, err, out)
		}
	}
}

// testServerExtendedConnect checks that the server advertises extended CONNECT
// and accepts requests with :protocol exactly when it is enabled.
func testServerExtendedConnect(t *testing.T, enabled bool) {
	gotProtocol := make(chan string, 1)
	st := newServerTester(t, func(w http.ResponseWriter, r *http.Request) {
		gotProtocol <- r.Header.Get(":protocol")
	})
	defer st.Close()

	advertised := false
	st.greetAndCheckSettings(func(s Setting) error {
		advertised = advertised || s.ID == SettingEnableConnectProtocol && s.Val == 1
		return nil
	})
	if advertised != enabled {
		t.Fatalf("SETTINGS_ENABLE_CONNECT_PROTOCOL advertised = %v, want %v", advertised, enabled)
	}

	st.bodylessReq1(":method", "CONNECT", ":protocol", "websocket")
	if !enabled {
		st.wantRSTStream(1, ErrCodeProtocol)
		return
	}
	select {
	case p := <-gotProtocol:
		if p != "websocket" {
			t.Fatalf(":protocol = %q, want %q", p, "websocket")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler was not called for an extended CONNECT request")
	}
}
