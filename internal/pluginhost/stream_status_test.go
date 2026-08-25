package pluginhost

import (
	"context"
	"testing"
	"time"
)

func TestStreamBridgeStatusTracksClientCancellation(t *testing.T) {
	bridge := newStreamBridge()
	ctx, cancel := context.WithCancel(context.Background())
	id, _, cleanup := bridge.open(ctx)
	defer cleanup()
	if !bridge.active(id) {
		t.Fatal("new stream is not active")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !bridge.active(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("canceled stream remained active")
}
