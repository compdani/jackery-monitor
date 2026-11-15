package bridge

import (
	"context"
	"errors"
	"testing"
)

func TestNativeQueuedCancelledCommand(t *testing.T) {
	n := &Native{}
	n.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() { close(started); _, err := n.Call(ctx, "set_output", map[string]any{"on": true}); result <- err }()
	<-started
	cancel()
	n.mu.Unlock()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
