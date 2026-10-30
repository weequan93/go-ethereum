// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/metrics"
	"golang.org/x/time/rate"
)

const compactionWriteBurst = 64 << 10

// compactionPacingFS paces only files identified by TableCreated as compaction
// output. Pebble emits that event after Create and before constructing the SST
// writer, outside its DB mutex. WAL, manifests, flushes and foreground reads use
// the original filesystem operations. This is an output-write budget, not a
// bound on compaction input reads, readahead, fsync latency or total device I/O.
type compactionPacingFS struct {
	vfs.FS
	limiter *rate.Limiter

	mu      sync.Mutex
	files   map[string]*compactionOutputFile
	ctx     context.Context
	cancel  context.CancelFunc
	stalled bool
	stopped bool

	bytes  *metrics.Meter
	wait   *metrics.Meter
	bypass *metrics.Meter
}

func newCompactionPacingFS(fs vfs.FS, bytesPerSecond int64, namespace string) *compactionPacingFS {
	ctx, cancel := context.WithCancel(context.Background())
	return &compactionPacingFS{
		FS: fs, limiter: rate.NewLimiter(rate.Limit(bytesPerSecond), compactionWriteBurst),
		files: make(map[string]*compactionOutputFile), ctx: ctx, cancel: cancel,
		bytes:  metrics.GetOrRegisterMeter(namespace+"compact/pacing/output", nil),
		wait:   metrics.GetOrRegisterMeter(namespace+"compact/pacing/wait", nil),
		bypass: metrics.GetOrRegisterMeter(namespace+"compact/pacing/bypass", nil),
	}
}

func (fs *compactionPacingFS) Create(name string) (vfs.File, error) {
	file, err := fs.FS.Create(name)
	if err != nil || !strings.HasSuffix(name, ".sst") {
		return file, err
	}
	f := &compactionOutputFile{File: file, fs: fs, name: filepath.Clean(name)}
	fs.mu.Lock()
	fs.files[f.name] = f
	fs.mu.Unlock()
	return f, nil
}

// markCompaction must never wait: event callbacks may be invoked with internal
// Pebble locks held. Only later writes, outside those locks, perform pacing.
func (fs *compactionPacingFS) markCompaction(info pebble.TableCreateInfo) {
	if info.Reason != "compacting" {
		return
	}
	fs.mu.Lock()
	if file := fs.files[filepath.Clean(info.Path)]; file != nil {
		file.compacting.Store(true)
	}
	fs.mu.Unlock()
}

// setWriteStalled releases existing waiters as well as bypassing future waits.
// This lets compaction make progress when Pebble needs it to unblock writers.
func (fs *compactionPacingFS) setWriteStalled(stalled bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.stopped || fs.stalled == stalled {
		return
	}
	fs.cancel()
	fs.ctx, fs.cancel = context.WithCancel(context.Background())
	fs.stalled = stalled
}

func (fs *compactionPacingFS) stop() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.stopped = true
	fs.cancel()
}

func (fs *compactionPacingFS) pace(size int) {
	fs.mu.Lock()
	ctx, bypass := fs.ctx, fs.stalled || fs.stopped
	fs.mu.Unlock()
	if bypass {
		fs.bypass.Mark(1)
		return
	}
	start := time.Now()
	if err := fs.limiter.WaitN(ctx, size); err != nil {
		// Cancellation means shutdown or urgent write-stall progress. It must
		// not become a synthetic storage error or cancel the compaction.
		fs.bypass.Mark(1)
	}
	fs.wait.Mark(int64(time.Since(start)))
}

type compactionOutputFile struct {
	vfs.File
	fs         *compactionPacingFS
	name       string
	compacting atomic.Bool
}

func (file *compactionOutputFile) Write(data []byte) (int, error) {
	if len(data) == 0 || !file.compacting.Load() {
		return file.File.Write(data)
	}
	var written int
	for len(data) > 0 {
		size := min(len(data), compactionWriteBurst)
		file.fs.pace(size)
		n, err := file.File.Write(data[:size])
		written += n
		file.fs.bytes.Mark(int64(n))
		if err != nil || n != size {
			// Preserve short-write/error behavior for Pebble's writer to handle.
			return written, err
		}
		data = data[size:]
	}
	return written, nil
}

func (file *compactionOutputFile) Close() error {
	file.fs.mu.Lock()
	if file.fs.files[file.name] == file {
		delete(file.fs.files, file.name)
	}
	file.fs.mu.Unlock()
	return file.File.Close()
}
