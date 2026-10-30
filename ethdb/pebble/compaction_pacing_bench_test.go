//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/vfs"
)

// sharedDeviceFS is an explicitly synthetic device: reads, WAL/flush writes and
// compaction writes share one serial byte-service budget over an in-memory FS.
// It permits reproducible contention without loading a production block device.
// It does not model kernel caching, fsync, cloud IOPS or a particular CBS tier.
type sharedDeviceFS struct {
	vfs.FS
	mu        sync.Mutex
	enabled   atomic.Bool
	bytes     atomic.Int64
	bytesPerS int64
}

func (fs *sharedDeviceFS) serve(size int) {
	if !fs.enabled.Load() {
		return
	}
	fs.mu.Lock()
	time.Sleep(time.Duration(float64(size) / float64(fs.bytesPerS) * float64(time.Second)))
	fs.bytes.Add(int64(size))
	fs.mu.Unlock()
}

func (fs *sharedDeviceFS) Create(name string) (vfs.File, error) {
	file, err := fs.FS.Create(name)
	if err != nil {
		return nil, err
	}
	return &sharedDeviceFile{File: file, fs: fs}, nil
}

func (fs *sharedDeviceFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	file, err := fs.FS.Open(name, opts...)
	if err != nil {
		return nil, err
	}
	return &sharedDeviceFile{File: file, fs: fs}, nil
}

func (fs *sharedDeviceFS) ReuseForWrite(old, name string) (vfs.File, error) {
	file, err := fs.FS.ReuseForWrite(old, name)
	if err != nil {
		return nil, err
	}
	return &sharedDeviceFile{File: file, fs: fs}, nil
}

type sharedDeviceFile struct {
	vfs.File
	fs *sharedDeviceFS
}

func (file *sharedDeviceFile) Write(data []byte) (int, error) {
	file.fs.serve(len(data))
	return file.File.Write(data)
}

func (file *sharedDeviceFile) ReadAt(data []byte, offset int64) (int, error) {
	file.fs.serve(len(data))
	return file.File.ReadAt(data, offset)
}

// BenchmarkCompactionPacingMixed compares the pacing policy under the same
// synthetic device and offered workload. Each b.N iteration observes six
// seconds of random foreground reads plus automatic compaction from writes.
// Rates here are model inputs, not recommended production settings.
func BenchmarkCompactionPacingMixed(b *testing.B) {
	for _, setting := range []struct {
		name string
		rate int64
	}{{"disabled", 0}, {"512KiB-per-second", 512 << 10}, {"1MiB-per-second", 1 << 20}} {
		b.Run(setting.name, func(b *testing.B) {
			for iteration := 0; iteration < b.N; iteration++ {
				runMixedPacingBenchmark(b, setting.rate)
			}
		})
	}
}

func runMixedPacingBenchmark(b *testing.B, outputRate int64) {
	const observation = 6 * time.Second
	device := &sharedDeviceFS{FS: vfs.NewMem(), bytesPerS: 8 << 20}
	var pacing *compactionPacingFS
	var fs vfs.FS = device
	var started, stalls, committedBytes, committedBatches atomic.Int64
	listener := &pebble.EventListener{
		CompactionBegin: func(pebble.CompactionInfo) {
			if device.enabled.Load() {
				started.Add(1)
			}
		},
		WriteStallBegin: func(pebble.WriteStallBeginInfo) {
			if device.enabled.Load() {
				stalls.Add(1)
			}
			if pacing != nil {
				pacing.setWriteStalled(true)
			}
		},
		WriteStallEnd: func() {
			if pacing != nil && device.enabled.Load() {
				pacing.setWriteStalled(false)
			}
		},
	}
	if outputRate > 0 {
		pacing = newCompactionPacingFS(device, outputRate, fmt.Sprintf("mixed-benchmark/%d/", outputRate))
		defer pacing.stop()
		pacing.setWriteStalled(true) // Setup uses the same unpaced contents in every mode.
		fs = pacing
		listener.TableCreated = pacing.markCompaction
	}
	cache := pebble.NewCache(0) // Force actual foreground read calls in this model.
	defer cache.Unref()
	db, err := pebble.Open("db", &pebble.Options{
		FS: fs, Cache: cache, EventListener: listener,
		MemTableSize: 256 << 10, MemTableStopWritesThreshold: 8,
		L0CompactionThreshold: 2, L0StopWritesThreshold: 50,
		LBaseMaxBytes: 1 << 20, MaxConcurrentCompactions: func() int { return 1 },
		Levels: []pebble.LevelOptions{{BlockSize: 4096, TargetFileSize: 128 << 10, Compression: pebble.NoCompression}},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	static := make(map[string][]byte)
	written := make(map[string][]byte)
	rng := rand.New(rand.NewSource(7))
	for _, group := range []struct {
		prefix string
		count  int
		values map[string][]byte
	}{{"read", 256, static}, {"write", 512, written}} {
		for i := 0; i < group.count; i++ {
			key := fmt.Sprintf("%s-%04d", group.prefix, i)
			value := make([]byte, 4096)
			rng.Read(value)
			group.values[key] = bytes.Clone(value)
			if err := db.Set([]byte(key), value, pebble.NoSync); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := db.Flush(); err != nil {
		b.Fatal(err)
	}
	if err := db.Compact([]byte("read"), []byte("write~"), false); err != nil {
		b.Fatal(err)
	}
	// Setup is outside the observation. Start with a full token bucket after
	// any setup compaction; no rate wait is necessary during setup.
	if pacing != nil {
		pacing.setWriteStalled(true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workers sync.WaitGroup
	latencies := make([]time.Duration, 0, 2048)
	errors := make(chan error, 2)
	device.bytes.Store(0)
	device.enabled.Store(true)
	if pacing != nil {
		pacing.setWriteStalled(false)
	}
	beginning := time.Now()
	workers.Add(2)
	go func() {
		defer workers.Done()
		random := rand.New(rand.NewSource(8))
		for ctx.Err() == nil {
			key := fmt.Sprintf("read-%04d", random.Intn(256))
			start := time.Now()
			value, closer, err := db.Get([]byte(key))
			if err != nil {
				errors <- err
				return
			}
			valid := bytes.Equal(value, static[key])
			closer.Close()
			if !valid {
				errors <- fmt.Errorf("foreground value changed for %s", key)
				return
			}
			latencies = append(latencies, time.Since(start))
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	go func() {
		defer workers.Done()
		random := rand.New(rand.NewSource(9))
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			batch := db.NewBatch()
			pending := make(map[string][]byte)
			for i := 0; i < 32; i++ {
				key := fmt.Sprintf("write-%04d", random.Intn(512))
				value := make([]byte, 4096)
				random.Read(value)
				pending[key] = bytes.Clone(value)
				if err := batch.Set([]byte(key), value, nil); err != nil {
					batch.Close()
					errors <- err
					return
				}
			}
			if err := batch.Commit(pebble.NoSync); err != nil {
				batch.Close()
				errors <- err
				return
			}
			batch.Close()
			committedBytes.Add(32 * 4096)
			committedBatches.Add(1)
			for key, value := range pending {
				written[key] = value
			}
		}
	}()
	time.Sleep(observation)
	// Snapshot the observation before disabling model delays for cleanup.
	elapsed, served := time.Since(beginning), device.bytes.Load()
	logicalBytes, batches := committedBytes.Load(), committedBatches.Load()
	stats := db.Metrics()
	cancel()
	device.enabled.Store(false)
	if pacing != nil {
		pacing.stop()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		b.Fatal(err)
	}
	if started.Load() == 0 || len(latencies) == 0 {
		b.Fatal("mixed observation had no automatic compaction or foreground reads")
	}
	for _, values := range []map[string][]byte{static, written} {
		for key, expected := range values {
			value, closer, err := db.Get([]byte(key))
			if err != nil {
				b.Fatal(err)
			}
			valid := bytes.Equal(value, expected)
			closer.Close()
			if !valid {
				b.Fatalf("final value changed for %s", key)
			}
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	quantile := func(fraction float64) float64 {
		return float64(latencies[int(float64(len(latencies)-1)*fraction)]) / float64(time.Millisecond)
	}
	b.ReportMetric(quantile(.50), "read-p50-ms")
	b.ReportMetric(quantile(.95), "read-p95-ms")
	b.ReportMetric(quantile(.99), "read-p99-ms")
	b.ReportMetric(float64(len(latencies))/elapsed.Seconds(), "reads/s")
	b.ReportMetric(float64(served)/(1<<20)/elapsed.Seconds(), "model-MiB/s")
	b.ReportMetric(float64(logicalBytes)/(1<<20), "background-write-MiB")
	b.ReportMetric(float64(batches), "background-batches")
	b.ReportMetric(float64(started.Load()), "compactions-started")
	b.ReportMetric(float64(stats.Compact.EstimatedDebt)/(1<<20), "debt-MiB")
	b.ReportMetric(float64(stats.Levels[0].Sublevels), "L0-sublevels")
	b.ReportMetric(float64(stalls.Load()), "write-stalls")
}
