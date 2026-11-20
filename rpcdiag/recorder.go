// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

// Package rpcdiag exposes the small context bridge used by RPC diagnostics.
// The recorder is installed only on requests for which diagnostics are enabled.
// Callers can therefore add named timing points without depending on the RPC
// server's internal trace representation.
package rpcdiag

import (
	"context"
	"time"
)

// Recorder receives named, request-local timing samples.
// Implementations must tolerate concurrent calls and must treat unknown names
// as no-ops.
type Recorder interface {
	Begin(name string) time.Time
	EndNamed(name string, started time.Time)
}

// TxQueueObservation measures the wait of one request up to its result or
// cancellation. A sequencing span covers the candidate batch as a whole.
type TxQueueObservation struct {
	Dequeued     bool
	FirstDequeue time.Duration
	NonceWait    time.Duration
	SequenceWait time.Duration
	LastSeqID    uint64
}

// TxQueueRecorder is the optional extension used by the Nitro sequencer.
type TxQueueRecorder interface {
	RecordTxQueue(TxQueueObservation)
}

type recorderKey struct{}

// WithRecorder attaches a diagnostics recorder to a request context.
func WithRecorder(ctx context.Context, recorder Recorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, recorderKey{}, recorder)
}

// FromContext returns the request-local recorder, if diagnostics are enabled.
func FromContext(ctx context.Context) Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(recorderKey{}).(Recorder)
	return recorder
}

// StartNamed starts a named timing point and returns a completion function.
// The returned function is always non-nil, which keeps instrumented paths
// branch-free at their call sites.
func StartNamed(ctx context.Context, name string) func() {
	recorder := FromContext(ctx)
	if recorder == nil {
		return func() {}
	}
	started := recorder.Begin(name)
	return func() { recorder.EndNamed(name, started) }
}
