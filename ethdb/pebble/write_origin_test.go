// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library, licensed under the GNU LGPL v3.

//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
)

func TestWriteOriginTracksWALFlushAndCompaction(t *testing.T) {
	namespace := t.Name() + "/"
	counter := func(origin string) int64 {
		return metrics.GetOrRegisterCounter(namespace+"write/origin/"+origin+"/bytes", nil).Snapshot().Count()
	}
	startWAL, startFlush, startCompaction := counter("wal"), counter("flush"), counter("compaction")
	db, err := New(t.TempDir(), 16, 16, namespace, false, &ExtraOptions{
		SyncMode: true, DisableAutomaticCompactions: true, WriteOriginMetrics: true,
		CompactionOutputBytesPerSecond: 64 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for version := 0; version < 2; version++ {
		for i := 0; i < 50; i++ {
			key := []byte(fmt.Sprintf("key%d", i))
			value := []byte(strings.Repeat(fmt.Sprint(version), 256))
			if err := db.Put(key, value); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.db.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if counter("wal") <= startWAL || counter("flush") <= startFlush || counter("compaction") != startCompaction {
		t.Fatalf("after flushes: WAL=%d, flush=%d, compaction=%d",
			counter("wal")-startWAL, counter("flush")-startFlush, counter("compaction")-startCompaction)
	}
	if err := db.db.Compact([]byte("a"), []byte("z"), false); err != nil {
		t.Fatal(err)
	}
	if counter("compaction") <= startCompaction {
		t.Fatal("compaction output was not classified")
	}
	value, err := db.Get([]byte("key0"))
	if err != nil || string(value) != strings.Repeat("1", 256) {
		t.Fatalf("expected the last written value, len=%d err=%v", len(value), err)
	}
}
