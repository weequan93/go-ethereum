//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/dbtest"
)

func TestCompactionPacingClassification(t *testing.T) {
	for _, reason := range []string{"flushing", "ingesting", "compacting"} {
		t.Run(reason, func(t *testing.T) {
			fs := newCompactionPacingFS(vfs.NewMem(), 1<<30, "pacing/"+t.Name()+"/")
			defer fs.stop()
			file, err := fs.Create("000001.sst")
			if err != nil {
				t.Fatal(err)
			}
			fs.markCompaction(pebble.TableCreateInfo{Path: "000001.sst", Reason: reason})
			if got := file.(*compactionOutputFile).compacting.Load(); got != (reason == "compacting") {
				t.Fatalf("classification = %v", got)
			}
			data := bytes.Repeat([]byte("content"), 30000)
			want := bytes.Clone(data) // vfs.File.Write may modify the input buffer.
			if n, err := file.Write(data); err != nil || n != len(data) {
				t.Fatalf("write: %d, %v", n, err)
			}
			got := make([]byte, len(data))
			if _, err := file.ReadAt(got, 0); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("changed file data: %v", err)
			}
			if err := file.Sync(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if len(fs.files) != 0 {
				t.Fatal("closed output retained")
			}
			wal, err := fs.Create("000002.log")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := wal.(*compactionOutputFile); ok {
				t.Fatal("WAL wrapped for pacing")
			}
			wal.Close()
		})
	}
}

func waitForPacingReservation(t *testing.T, fs *compactionPacingFS) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fs.limiter.Tokens() >= 0 {
		if time.Now().After(deadline) {
			t.Fatal("write never reserved pacing tokens")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCompactionPacingUrgentProgressAndShutdown(t *testing.T) {
	for _, release := range []string{"write-stall", "shutdown"} {
		t.Run(release, func(t *testing.T) {
			fs := newCompactionPacingFS(vfs.NewMem(), 1, "pacing/"+t.Name()+"/")
			defer fs.stop()
			file, err := fs.Create("000001.sst")
			if err != nil {
				t.Fatal(err)
			}
			fs.markCompaction(pebble.TableCreateInfo{Path: "000001.sst", Reason: "compacting"})
			if !fs.limiter.AllowN(time.Now(), compactionWriteBurst) {
				t.Fatal("cannot consume initial burst")
			}
			done := make(chan error, 1)
			go func() { _, err := file.Write([]byte("unchanged")); done <- err }()
			waitForPacingReservation(t, fs)
			// Foreground operations and unrelated flush writes must not share the wait.
			flush, err := fs.Create("000002.sst")
			if err != nil {
				t.Fatal(err)
			}
			fs.markCompaction(pebble.TableCreateInfo{Path: "000002.sst", Reason: "flushing"})
			if _, err := flush.Write([]byte("flush")); err != nil {
				t.Fatal(err)
			}
			flush.Close()
			if release == "write-stall" {
				db := &Database{compactionPacing: fs, namespace: "pacing/urgent/"}
				db.onWriteStallBegin(pebble.WriteStallBeginInfo{Reason: "L0 file count limit exceeded"})
			} else {
				fs.stop()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("pacing did not release")
			}
			got := make([]byte, 9)
			if _, err := file.ReadAt(got, 0); err != nil || string(got) != "unchanged" {
				t.Fatalf("data=%q err=%v", got, err)
			}
			if release == "write-stall" {
				fs.setWriteStalled(false)
				go func() { _, err := file.Write([]byte("again")); done <- err }()
				waitForPacingReservation(t, fs)
				fs.stop()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("resumed pacing did not release")
				}
			}
			file.Close()
		})
	}
}

func TestCompactionPacingForegroundReadAndClose(t *testing.T) {
	directory := t.TempDir()
	opts := &ExtraOptions{CompactionOutputBytesPerSecond: 1, DisableAutomaticCompactions: true}
	db, err := New(directory, 16, 16, "pacing/close/", false, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	want := make(map[string][]byte)
	rng := rand.New(rand.NewSource(4))
	for generation := 0; generation < 3; generation++ {
		for i := 0; i < 16; i++ {
			key := fmt.Sprintf("key-%04d", i)
			value := make([]byte, 32<<10)
			rng.Read(value)
			want[key] = bytes.Clone(value)
			if err := db.Put([]byte(key), value); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.db.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	compacted := make(chan error, 1)
	go func() { compacted <- db.db.Compact([]byte("key-"), []byte("key~"), false) }()
	waitForPacingReservation(t, db.compactionPacing)
	read := make(chan error, 1)
	go func() {
		got, err := db.Get([]byte("key-0000"))
		if err == nil && !bytes.Equal(got, want["key-0000"]) {
			err = fmt.Errorf("foreground read changed")
		}
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("paced compaction blocked foreground reads")
	}
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release paced compaction")
	}
	select {
	case err := <-compacted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compaction caller did not finish")
	}
	reopened, err := New(directory, 16, 16, "pacing/reopen/", true, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.compactionPacing != nil {
		t.Fatal("read-only database installed pacing")
	}
	for key, value := range want {
		got, err := reopened.Get([]byte(key))
		if err != nil || !bytes.Equal(got, value) {
			t.Fatalf("reopen changed %s: %v", key, err)
		}
	}
}

type shortWriteFile struct {
	vfs.File
	err error
}

func (f shortWriteFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p[:min(3, len(p))])
	if err == nil {
		err = f.err
	}
	return n, err
}

func TestCompactionPacingPreservesShortWrites(t *testing.T) {
	fs := newCompactionPacingFS(vfs.NewMem(), 1<<30, "pacing/short/")
	defer fs.stop()
	raw, err := fs.FS.Create("000001.sst")
	if err != nil {
		t.Fatal(err)
	}
	file := &compactionOutputFile{File: shortWriteFile{File: raw}, fs: fs, name: "000001.sst"}
	file.compacting.Store(true)
	if n, err := file.Write([]byte("abcdef")); n != 3 || err != nil {
		t.Fatalf("short write changed: %d %v", n, err)
	}
	wantErr := fmt.Errorf("injected storage error")
	file.File = shortWriteFile{File: raw, err: wantErr}
	if n, err := file.Write([]byte("abcdef")); n != 3 || err != wantErr {
		t.Fatalf("storage error changed: %d %v", n, err)
	}
	file.Close()
}

func TestCompactionPacingDatabaseResultsAndReopen(t *testing.T) {
	fs := newCompactionPacingFS(vfs.NewMem(), 64<<20, "pacing/database/")
	defer fs.stop()
	opts := &pebble.Options{FS: fs, MemTableSize: 64 << 10, MemTableStopWritesThreshold: 4, DisableAutomaticCompactions: true,
		EventListener: &pebble.EventListener{TableCreated: fs.markCompaction},
		Levels:        []pebble.LevelOptions{{TargetFileSize: 32 << 10, Compression: pebble.NoCompression}}}
	db, err := pebble.Open("db", opts)
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string][]byte)
	rng := rand.New(rand.NewSource(1))
	for generation := 0; generation < 3; generation++ {
		for i := 0; i < 48; i++ {
			key := fmt.Sprintf("key-%04d", i)
			value := make([]byte, 4096)
			rng.Read(value)
			values[key] = value
			if err := db.Set([]byte(key), value, pebble.NoSync); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Compact([]byte("key-"), []byte("key~"), false); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		for key, value := range values {
			got, closer, err := db.Get([]byte(key))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, value) {
				t.Fatalf("value changed for %s", key)
			}
			closer.Close()
		}
	}
	check()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if len(fs.files) != 0 {
		t.Fatal("output handles leaked")
	}
	db, err = pebble.Open("db", opts)
	if err != nil {
		t.Fatal(err)
	}
	check()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionPacingDatabaseSuite(t *testing.T) {
	var next int
	var mu sync.Mutex
	dbtest.TestDatabaseSuite(t, func() ethdb.KeyValueStore {
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		db, err := New(t.TempDir(), 16, 16, fmt.Sprintf("pacing/suite/%d/", id), false,
			&ExtraOptions{CompactionOutputBytesPerSecond: 64 << 20})
		if err != nil {
			t.Fatal(err)
		}
		return db
	})
}

// Ensure the wrapped file still satisfies the full vfs API; reads/syncs are
// forwarded unchanged through its embedded File.
var _ vfs.File = (*compactionOutputFile)(nil)
var _ io.Writer = (*compactionOutputFile)(nil)
