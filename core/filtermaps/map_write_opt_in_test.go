package filtermaps

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/rpcdiag"
	"github.com/ethereum/go-ethereum/metrics"
)

func runOptInWrite(t *testing.T, ts *testSetup, configured bool) (*FilterMaps, int) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	t.Cleanup(func() { db.Close() })
	f, err := NewFilterMaps(db, writeDiagnosticsView(ts, 1), 0, 0, DefaultParams, Config{WriteDiagnostics: configured})
	if err != nil {
		t.Fatal(err)
	}
	if f.writeDiagnostics != configured {
		t.Fatal("per-indexer configuration lost")
	}
	if err := f.init(); err != nil {
		t.Fatal(err)
	}
	r, err := f.renderMapsBefore(math.MaxUint32)
	if err != nil || r == nil {
		t.Fatal("renderer unavailable", err)
	}
	records := 0
	r.testWriteObserver = func(record mapWriteRecord) {
		records++
		if record.counts[writeDetailClockCalls] == 0 || record.counts[writeRowsVisited] != uint64(f.mapHeight) {
			t.Fatal("enabled write did not observe row work")
		}
	}
	if done, err := r.run(func() bool { return false }, func() {}); err != nil || !done {
		t.Fatal("write failed", done, err)
	}
	if len(f.baseRowAbsence.absent) != 8192/8 {
		t.Fatal("absence-cache optimization must remain active with diagnostics off")
	}
	return f, records
}

// Run this test by itself as well: metrics.Enable is process-global and earlier
// tests may have opted in. It proves --metrics alone plus historical RPC traces
// cannot enable the optional writer or allocate its output resources.
func TestMapWriteDiagnosticsNeverEnabledResources(t *testing.T) {
	if mapWriteObserved != nil || mapWriteLogQueue.records != nil {
		t.Skip("requires fresh process; separately run by the validation script")
	}
	if metrics.DefaultRegistry.Get("filtermaps/maps/write_diagnostics/observed") != nil {
		t.Fatal("write counter registered without opt-in")
	}
	metrics.Enable()
	rpcdiag.New(context.Background())
	ts := newTestSetup(t)
	t.Cleanup(ts.close)
	ts.chain.addBlocks(1, 1, 16, 3, false)
	_, records := runOptInWrite(t, ts, false)
	if records != 0 || mapWriteObserved != nil || mapWriteSlow != nil || mapWriteLogQueue.records != nil {
		t.Fatal("default-off writer collected or initialized diagnostics")
	}
	if got := testing.AllocsPerRun(1000, func() {
		// A nil FilterMaps deliberately proves that the off gate never even
		// peeks at the cache, let alone visits rows or creates detail wrappers.
		if newMapWriteObservation(false, nil, 0, 1, time.Time{}) != nil {
			panic("disabled observation allocated")
		}
	}); got != 0 {
		t.Fatalf("disabled observation allocations: %v", got)
	}
}

func TestMapWriteDiagnosticsExplicitFlag(t *testing.T) {
	metrics.Enable()
	ts := newTestSetup(t)
	t.Cleanup(ts.close)
	ts.chain.addBlocks(1, 1, 16, 3, false)
	plain, offRecords := runOptInWrite(t, ts, false)
	observed, onRecords := runOptInWrite(t, ts, true)
	if offRecords != 0 || onRecords == 0 {
		t.Fatal("explicit flag did not control write observation", offRecords, onRecords)
	}
	if writeDiagnosticsHash(t, plain.db) != writeDiagnosticsHash(t, observed.db) {
		t.Fatal("diagnostics flag changed database contents")
	}
}

// This is also run by itself so metrics have never been enabled.
func TestMapWriteDiagnosticsMetricsOff(t *testing.T) {
	if metrics.Enabled() {
		t.Skip("requires fresh process; separately run by the validation script")
	}
	ts := newTestSetup(t)
	t.Cleanup(ts.close)
	ts.chain.addBlocks(1, 1, 16, 3, false)
	_, records := runOptInWrite(t, ts, true)
	if records != 0 || mapWriteObserved != nil || mapWriteLogQueue.records != nil {
		t.Fatal("write diagnostics ignored metrics-off gate")
	}
}

func TestMapWriteDiagnosticsLazyOutput(t *testing.T) {
	metrics.Enable()
	emitMapWriteRecord(mapWriteRecord{}) // A fast explicitly observed write.
	before := mapWriteObserved.Snapshot().Count()
	emitMapWriteRecord(mapWriteRecord{})
	if cap(mapWriteLogQueue.records) != 16 || mapWriteObserved.Snapshot().Count() != before+1 || mapWriteSlow == nil {
		t.Fatal("opted-in output not initialized/bounded")
	}
}

func BenchmarkMapWriteDiagnosticsOffGate(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if newMapWriteObservation(false, nil, 0, 1, time.Time{}) != nil {
			b.Fatal("off gate allocated")
		}
	}
}
