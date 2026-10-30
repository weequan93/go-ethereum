// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

// Package rpcdiag contains optional request-local timing metadata. It never
// changes deadlines, cancellation, locking, data ownership, or returned values.
package rpcdiag

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type Stage uint8

const (
	Params Stage = iota
	Callback
	Encode
	Resolve
	BackendSetup
	SyncInitial
	IndexedSearch
	ResultFilter
	UnindexedSearch
	SyncPost
	Consistency
	BackendClose
	IndexLockWait
	IndexLockHold
	FilterRowsRead
	BlockPointerRead
	LookupLocate
	ReceiptsRead
	ReceiptScan
	HeaderRead
	BlockLogsRead
	BodyRead
	SyncRegisterWait
	SyncEnqueueWait
	SyncResponseWait
	stageCount
)

var stageNames = [stageCount]string{"params", "callback", "encode", "resolve", "backend_setup", "sync_initial", "indexed_search", "result_filter", "unindexed_search", "sync_post", "consistency", "backend_close", "index_lock_wait", "index_lock_hold", "filter_rows_read", "block_pointer_access", "log_lookup", "receipts_access", "receipt_scan", "header_access", "block_logs_access", "body_access", "sync_register_wait", "sync_enqueue_wait", "sync_response_wait"}

type Field uint8

const (
	ByHash Field = iota
	Addresses
	TopicPositions
	TopicAlternatives
	ResolvedBlocks
	IndexedBlockVisits
	UnindexedBlockVisits
	Candidates
	Results
	ReceiptReads
	ReceiptCursorHits
	LogCacheHits
	LogCacheMisses
	BodyCacheHits
	BodyCacheMisses
	FailureKind
	fieldCount
)

var fieldNames = [fieldCount]string{"by_hash", "address_count", "topic_positions", "topic_alternatives", "resolved_blocks_max", "indexed_block_range_total", "unindexed_blocks_scanned", "candidate_count", "result_count", "receipt_reads", "receipt_cursor_hits", "log_cache_hits", "log_cache_misses", "body_cache_hits", "body_cache_misses", "failure_kind"}

const (
	FailureOther    = 1
	FailureCanceled = 2
	FailureDeadline = 3
	FailureEncode   = 4
	FailureParams   = 5
)

// everEnabled keeps the never-enabled path to one atomic read. Once enabled,
// contexts are checked even after disabling so in-flight records remain intact.
var everEnabled atomic.Bool
var sequence atomic.Uint64

type contextKey struct{}
type span struct{ ns, calls, maximum atomic.Int64 }
type Trace struct {
	started time.Time
	id      uint64
	spans   [stageCount]span
	fields  [fieldCount]atomic.Int64
}

type Timing struct{ Nanoseconds, Calls, MaxNanoseconds int64 }
type Snapshot struct {
	ID     uint64
	Total  time.Duration
	Stages [stageCount]Timing
	Fields [fieldCount]int64
}

func New(ctx context.Context) (context.Context, *Trace) {
	everEnabled.Store(true)
	t := &Trace{started: time.Now(), id: sequence.Add(1)}
	return context.WithValue(ctx, contextKey{}, t), t
}
func FromContext(ctx context.Context) *Trace {
	if !everEnabled.Load() {
		return nil
	}
	t, _ := ctx.Value(contextKey{}).(*Trace)
	return t
}
func (t *Trace) Start() time.Time {
	if t == nil {
		return time.Time{}
	}
	return time.Now()
}
func (t *Trace) End(stage Stage, started time.Time) {
	if t != nil {
		t.AddTime(stage, time.Since(started))
	}
}
func (t *Trace) AddTime(stage Stage, duration time.Duration) {
	if t == nil || stage >= stageCount || duration < 0 {
		return
	}
	s := &t.spans[stage]
	s.ns.Add(int64(duration))
	s.calls.Add(1)
	for old := s.maximum.Load(); int64(duration) > old; old = s.maximum.Load() {
		if s.maximum.CompareAndSwap(old, int64(duration)) {
			break
		}
	}
}
func (t *Trace) Add(field Field, value int64) {
	if t != nil && field < fieldCount && value >= 0 {
		t.fields[field].Add(value)
	}
}
func (t *Trace) Max(field Field, value int64) {
	if t == nil || field >= fieldCount || value < 0 {
		return
	}
	p := &t.fields[field]
	for old := p.Load(); value > old; old = p.Load() {
		if p.CompareAndSwap(old, value) {
			return
		}
	}
}

func (t *Trace) Error(err error) {
	if t == nil || err == nil {
		return
	}
	kind := int64(FailureOther)
	// Avoid invoking arbitrary Error/Is/Unwrap methods for observation.
	if err == context.Canceled {
		kind = FailureCanceled
	}
	if err == context.DeadlineExceeded {
		kind = FailureDeadline
	}
	t.Max(FailureKind, kind)
}
func (t *Trace) Snapshot() Snapshot {
	result := Snapshot{ID: t.id, Total: time.Since(t.started)}
	for i := range result.Stages {
		s := &t.spans[i]
		result.Stages[i] = Timing{s.ns.Load(), s.calls.Load(), s.maximum.Load()}
	}
	for i := range result.Fields {
		result.Fields[i] = t.fields[i].Load()
	}
	return result
}

func (t *Trace) Elapsed() time.Duration { return time.Since(t.started) }

// RLock/RUnlock preserve the original lock operation/order. Only numeric timing
// is updated, after releasing the lock for hold time. No observer lock is held.
func (t *Trace) RLock(lock *sync.RWMutex) time.Time {
	if t == nil {
		lock.RLock()
		return time.Time{}
	}
	started := time.Now()
	lock.RLock()
	acquired := time.Now()
	t.AddTime(IndexLockWait, acquired.Sub(started))
	return acquired
}
func (t *Trace) RUnlock(lock *sync.RWMutex, acquired time.Time) {
	if t == nil {
		lock.RUnlock()
		return
	}
	end := time.Now()
	lock.RUnlock()
	t.AddTime(IndexLockHold, end.Sub(acquired))
}

// LogFields is generated only by the bounded log worker, not by fast requests.
// Nested backend/lock timings are cumulative operation work and may overlap
// callback/search wall times or exceed wall time when matcher workers overlap.
func (s Snapshot) LogFields() []any {
	fields := []any{"trace", s.ID, "total_ms", float64(s.Total) / float64(time.Millisecond)}
	rpcParts := s.Stages[Params].Nanoseconds + s.Stages[Callback].Nanoseconds + s.Stages[Encode].Nanoseconds
	other := int64(s.Total) - rpcParts
	if other < 0 {
		other = 0
	}
	fields = append(fields, "handler_other_ms", float64(other)/float64(time.Millisecond))
	for i, v := range s.Stages {
		if v.Calls == 0 {
			continue
		}
		fields = append(fields, stageNames[i]+"_ms", float64(v.Nanoseconds)/float64(time.Millisecond), stageNames[i]+"_calls", v.Calls, stageNames[i]+"_max_ms", float64(v.MaxNanoseconds)/float64(time.Millisecond))
	}
	for i, value := range s.Fields {
		fields = append(fields, fieldNames[i], value)
	}
	return fields
}
