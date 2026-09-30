package cli

import "testing"

func TestCanvasOnlyListensOnLoopback(t *testing.T) {
	for _, value := range []string{"127.0.0.1:7331", "localhost:0", "[::1]:7331"} {
		if !loopbackListenAddress(value) {
			t.Errorf("%q rejected", value)
		}
	}
	for _, value := range []string{"0.0.0.0:7331", "[::]:7331", "192.0.2.7:7331", "missing-port"} {
		if loopbackListenAddress(value) {
			t.Errorf("%q accepted", value)
		}
	}
}
