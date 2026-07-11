package rpc

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

type gatedWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestSubscriptionTryNotifyReportsBoundedQueueSaturation(t *testing.T) {
	done := make(chan struct{})
	writer := &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	cw := newConnWriter(json.NewEncoder(writer), done)
	sub := &Subscription{id: "s", w: cw, done: done}
	if err := sub.TryNotify("event", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	for i := 0; i < cap(cw.queue); i++ {
		if err := sub.TryNotify("event", map[string]any{"n": i + 1}); err != nil {
			t.Fatalf("queue filled early at %d: %v", i, err)
		}
	}
	if err := sub.TryNotify("event", map[string]any{"overflow": true}); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("want ErrSlowConsumer, got %v", err)
	}
	close(done)
	close(writer.release)
	<-cw.stopped
}
