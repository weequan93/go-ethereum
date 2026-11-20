package rpcdiag

import (
	"context"
	"testing"
	"time"
)

type testRecorder struct {
	name    string
	started time.Time
	ended   bool
}

func (r *testRecorder) Begin(name string) time.Time {
	r.name = name
	r.started = time.Now()
	return r.started
}

func (r *testRecorder) EndNamed(name string, started time.Time) {
	r.ended = name == r.name && started.Equal(r.started)
}

func TestStartNamedWithoutRecorderIsNoop(t *testing.T) {
	StartNamed(context.Background(), "tx_publish_queue_wait")()
}

func TestStartNamedUsesRequestRecorder(t *testing.T) {
	recorder := new(testRecorder)
	ctx := WithRecorder(context.Background(), recorder)
	StartNamed(ctx, "tx_publish_queue_wait")()
	if recorder.name != "tx_publish_queue_wait" || !recorder.ended {
		t.Fatalf("recorder did not receive named timing: %+v", recorder)
	}
}

func TestWithRecorderPreservesContextSemantics(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithRecorder(parent, new(testRecorder))
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("recorder changed cancellation semantics")
	}
}
