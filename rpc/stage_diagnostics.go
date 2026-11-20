// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.
package rpc

import (
	"context"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/internal/rpcdiag"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"golang.org/x/time/rate"
)

const stageQueueSize = 32

var stageSubsecondThresholds = [...]struct {
	name     string
	duration time.Duration
}{
	{"ge10ms", 10 * time.Millisecond},
	{"ge25ms", 25 * time.Millisecond},
	{"ge50ms", 50 * time.Millisecond},
	{"ge100ms", 100 * time.Millisecond},
	{"ge250ms", 250 * time.Millisecond},
	{"ge500ms", 500 * time.Millisecond},
}

type stageCounters struct {
	total, ge1s, ge3s, ge5s *metrics.Counter
	subsecond               [len(stageSubsecondThresholds)]*metrics.Counter
}
type stageConfig struct {
	threshold time.Duration
	limiter   *rate.Limiter
	counts    [2][2]stageCounters
	dropped   *metrics.Counter
}
type stageRecord struct {
	method, outcome string
	snapshot        rpcdiag.Snapshot
}
type stageOutput struct {
	records chan stageRecord
	stop    chan struct{}
	once    sync.Once
}

// SetStageDiagnosticsThreshold enables request-local diagnostics only for
// eth_getLogs and eth_sendRawTransaction. A non-positive value disables new
// traces. All completed instrumented calls are counted, including subsecond
// thresholds at 10/25/50/100/250/500ms and the existing >=1/3/5s thresholds.
// Detail logging uses a bounded, non-blocking queue and a shared1/s burst10
// limiter per server. This startup option does not change RPC execution.
func (s *Server) SetStageDiagnosticsThreshold(threshold time.Duration) {
	s.services.mu.Lock()
	defer s.services.mu.Unlock()
	if threshold <= 0 || !s.run.Load() {
		s.services.stageDiagnostics.Store(nil)
		return
	}
	if s.services.stageOutput == nil {
		output := &stageOutput{records: make(chan stageRecord, stageQueueSize), stop: make(chan struct{})}
		s.services.stageOutput = output
		go output.run(func(record stageRecord) {
			fields := []any{"method", record.method, "outcome", record.outcome}
			fields = append(fields, record.snapshot.LogFields()...)
			log.Warn("RPC stage timings", fields...)
		})
	}
	config := &stageConfig{threshold: threshold, limiter: rate.NewLimiter(1, 10), dropped: metrics.GetOrRegisterCounter("rpc/diagnostics/details/dropped", nil)}
	for mi, method := range []string{"eth_getLogs", "eth_sendRawTransaction"} {
		for oi, outcome := range []string{"success", "failure"} {
			prefix := "rpc/diagnostics/" + method + "/" + outcome + "/"
			counts := stageCounters{
				total: metrics.GetOrRegisterCounter(prefix+"completed", nil),
				ge1s:  metrics.GetOrRegisterCounter(prefix+"ge1s", nil),
				ge3s:  metrics.GetOrRegisterCounter(prefix+"ge3s", nil),
				ge5s:  metrics.GetOrRegisterCounter(prefix+"ge5s", nil),
			}
			for i, threshold := range stageSubsecondThresholds {
				counts.subsecond[i] = metrics.GetOrRegisterCounter(prefix+threshold.name, nil)
			}
			config.counts[mi][oi] = counts
		}
	}
	s.services.stageDiagnostics.Store(config)
}

func (o *stageOutput) run(write func(stageRecord)) {
	for {
		select {
		case <-o.stop:
			return
		default:
		}
		select {
		case <-o.stop:
			return
		case record := <-o.records:
			write(record)
		}
	}
}
func (r *serviceRegistry) stopStageDiagnostics() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stageDiagnostics.Store(nil)
	if r.stageOutput != nil {
		r.stageOutput.once.Do(func() { close(r.stageOutput.stop) })
	}
}
func (r *serviceRegistry) beginStageDiagnostics(ctx context.Context, method string) (context.Context, *rpcdiag.Trace, *stageConfig) {
	if method != "eth_getLogs" && method != "eth_sendRawTransaction" {
		return ctx, nil, nil
	}
	config := r.stageDiagnostics.Load()
	if config == nil {
		return ctx, nil, nil
	}
	observed, trace := rpcdiag.New(ctx)
	return observed, trace, config
}
func (r *serviceRegistry) finishStageDiagnostics(trace *rpcdiag.Trace, config *stageConfig, method string, failed bool) {
	duration := trace.Elapsed()
	mi, oi := 0, 0
	if method == "eth_sendRawTransaction" {
		mi = 1
	}
	outcome := "success"
	if failed {
		oi = 1
		outcome = "failure"
	}
	config.counts[mi][oi].complete(duration)
	if duration < config.threshold {
		return
	}
	if !config.limiter.Allow() {
		config.dropped.Inc(1)
		return
	}
	// Output is initialized before config is published and is immutable until
	// server shutdown. Never close records: in-flight producers may still finish.
	output := r.stageOutput
	select {
	case <-output.stop:
		config.dropped.Inc(1)
		return
	default:
	}
	snapshot := trace.Snapshot()
	snapshot.Total = duration
	select {
	case output.records <- stageRecord{method: method, outcome: outcome, snapshot: snapshot}:
	default:
		config.dropped.Inc(1)
	}
}

func (c stageCounters) complete(duration time.Duration) {
	c.total.Inc(1)
	for i, threshold := range stageSubsecondThresholds {
		if duration >= threshold.duration {
			c.subsecond[i].Inc(1)
		}
	}
	if duration >= time.Second {
		c.ge1s.Inc(1)
	}
	if duration >= 3*time.Second {
		c.ge3s.Inc(1)
	}
	if duration >= 5*time.Second {
		c.ge5s.Inc(1)
	}
}
