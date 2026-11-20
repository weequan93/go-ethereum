// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

package filtermaps

import (
	"sync"
	"time"
)

// tempRangeTimer integrates the wall time from entering a temporary range
// through the final batch commit. Two readings give the overlap with a wait,
// even when the request is canceled before the indexer publishes the range.
type tempRangeTimer struct {
	mu        sync.Mutex
	started   time.Time
	completed time.Duration
}

func (t *tempRangeTimer) set(active bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if active && t.started.IsZero() {
		t.started = time.Now()
	} else if !active && !t.started.IsZero() {
		t.completed += time.Since(t.started)
		t.started = time.Time{}
	}
}

func (t *tempRangeTimer) sample() (time.Time, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	if !t.started.IsZero() {
		return now, t.completed + now.Sub(t.started)
	}
	return now, t.completed
}
