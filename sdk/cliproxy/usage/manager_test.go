package usage

import (
	"context"
	"testing"
	"time"
)

type blockingPlugin struct {
	started chan struct{}
	release chan struct{}
}

// HandleUsage blocks after signaling so queue backpressure can be observed deterministically.
func (p *blockingPlugin) HandleUsage(context.Context, Record) {
	select {
	case p.started <- struct{}{}:
	default:
	}
	<-p.release
}

// TestManagerPublishBlocksWhenQueueIsFull proves the constructor buffer protects against unbounded memory growth.
func TestManagerPublishBlocksWhenQueueIsFull(t *testing.T) {
	manager := NewManager(1)
	plugin := &blockingPlugin{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	manager.Register(plugin)
	defer manager.Stop()
	defer close(plugin.release)

	manager.Publish(context.Background(), Record{Model: "first"})
	select {
	case <-plugin.started:
	case <-time.After(time.Second):
		t.Fatal("first record did not reach plugin")
	}

	manager.Publish(context.Background(), Record{Model: "second"})

	published := make(chan struct{})
	go func() {
		manager.Publish(context.Background(), Record{Model: "third"})
		close(published)
	}()

	select {
	case <-published:
		t.Fatal("Publish returned while the bounded queue was full")
	case <-time.After(50 * time.Millisecond):
	}
}
