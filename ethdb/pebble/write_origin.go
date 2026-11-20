// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/metrics"
)

type writeOrigin uint32

const (
	originOther writeOrigin = iota
	originCompaction
	originFlush
	originWAL
	originManifest
	originCount
)

// writeOriginFS counts bytes accepted by VFS writes. In particular, an SST
// suffix alone cannot distinguish a flush from a compaction.
type writeOriginFS struct {
	vfs.FS
	counters [originCount]*metrics.Counter
	mu       sync.Mutex
	tables   map[string]*writeOriginFile
}

func newWriteOriginFS(fs vfs.FS, namespace string) *writeOriginFS {
	result := &writeOriginFS{FS: fs, tables: make(map[string]*writeOriginFile)}
	for i, name := range [...]string{"other", "compaction", "flush", "wal", "manifest"} {
		result.counters[i] = metrics.GetOrRegisterCounter(namespace+"write/origin/"+name+"/bytes", nil)
	}
	return result
}

func (fs *writeOriginFS) Unwrap() vfs.FS { return fs.FS }

func (fs *writeOriginFS) wrap(file vfs.File, err error, path string) (vfs.File, error) {
	if err != nil {
		return file, err
	}
	name := filepath.Base(path)
	wrapped := &writeOriginFile{File: file, fs: fs}
	switch {
	case strings.HasSuffix(name, ".log"):
		wrapped.origin.Store(uint32(originWAL))
	case strings.HasPrefix(name, "MANIFEST-"):
		wrapped.origin.Store(uint32(originManifest))
	case strings.HasSuffix(name, ".sst"):
		wrapped.table = filepath.Clean(path)
		fs.mu.Lock()
		fs.tables[wrapped.table] = wrapped
		fs.mu.Unlock()
	}
	return wrapped, nil
}

func (fs *writeOriginFS) Create(name string) (vfs.File, error) {
	file, err := fs.FS.Create(name)
	return fs.wrap(file, err, name)
}

func (fs *writeOriginFS) OpenReadWrite(name string, options ...vfs.OpenOption) (vfs.File, error) {
	file, err := fs.FS.OpenReadWrite(name, options...)
	return fs.wrap(file, err, name)
}

func (fs *writeOriginFS) ReuseForWrite(oldName, newName string) (vfs.File, error) {
	file, err := fs.FS.ReuseForWrite(oldName, newName)
	return fs.wrap(file, err, newName)
}

func (fs *writeOriginFS) tableCreated(info pebble.TableCreateInfo) {
	var origin writeOrigin
	switch info.Reason {
	case "compacting":
		origin = originCompaction
	case "flushing":
		origin = originFlush
	default:
		return
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if file := fs.tables[filepath.Clean(info.Path)]; file != nil {
		file.origin.Store(uint32(origin))
	}
}

type writeOriginFile struct {
	vfs.File
	fs     *writeOriginFS
	table  string
	origin atomic.Uint32
}

func (f *writeOriginFile) count(n int) {
	if n > 0 {
		f.fs.counters[f.origin.Load()].Inc(int64(n))
	}
}

func (f *writeOriginFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	f.count(n)
	return n, err
}

func (f *writeOriginFile) WriteAt(p []byte, offset int64) (int, error) {
	n, err := f.File.WriteAt(p, offset)
	f.count(n)
	return n, err
}

func (f *writeOriginFile) Close() error {
	if f.table != "" {
		f.fs.mu.Lock()
		if f.fs.tables[f.table] == f {
			delete(f.fs.tables, f.table)
		}
		f.fs.mu.Unlock()
	}
	return f.File.Close()
}
