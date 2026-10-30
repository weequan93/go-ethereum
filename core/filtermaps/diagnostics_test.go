package filtermaps

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/internal/rpcdiag"
)

func TestLookupDiagnosticsPreserveResultsReadsAndRequestIsolation(t *testing.T) {
	ts, chain := newLookupSetup(t, 16)
	indices, expected := lookupBlockIndices(t, ts.fm, 2)
	for _, enabled := range []bool{false, true, true} {
		ctx := context.Background()
		var trace *rpcdiag.Trace
		if enabled {
			ctx, trace = rpcdiag.New(ctx)
		}
		mb := ts.fm.NewMatcherBackend()
		chain.reads.Store(0)
		matcher := matcherEnv{ctx: ctx, backend: mb, lastIndex: math.MaxUint64}
		got, err := matcher.getLogsFromMatches(indices)
		mb.Close()
		if err != nil || !reflect.DeepEqual(got, expected) || chain.reads.Load() != 1 {
			t.Fatalf("instrumentation changed lookup: logs=%d err=%v reads=%d", len(got), err, chain.reads.Load())
		}
		if trace != nil {
			s := trace.Snapshot()
			if s.Fields[rpcdiag.ReceiptReads] != 1 || s.Fields[rpcdiag.ReceiptCursorHits] != int64(len(indices)-1) {
				t.Fatalf("wrong read/cursor metadata: %v", s.Fields)
			}
			if s.Stages[rpcdiag.IndexLockWait].Calls != int64(len(indices)) || s.Stages[rpcdiag.IndexLockHold].Calls != int64(len(indices)) {
				t.Fatal("index lock observations missing")
			}
		}
	}
}

func TestLookupDiagnosticsKeepReceiptErrorRetry(t *testing.T) {
	ts, chain := newLookupSetup(t, 4)
	indices, expected := lookupBlockIndices(t, ts.fm, 2)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	_, trace := rpcdiag.New(context.Background())
	var cache logLookupCache
	chain.reads.Store(0)
	chain.missing.Store(true)
	if got, err := mb.getLogByLvIndexObserved(indices[0], &cache, trace); err == nil || got != nil {
		t.Fatal("receipt error was changed")
	}
	chain.missing.Store(false)
	if got, err := mb.getLogByLvIndexObserved(indices[0], &cache, trace); err != nil || got != expected[0] {
		t.Fatal("receipt retry was changed")
	}
	if chain.reads.Load() != 2 || trace.Snapshot().Fields[rpcdiag.ReceiptReads] != 2 {
		t.Fatal("failed read cached or read counter changed")
	}
}

func BenchmarkLookupDiagnostics(b *testing.B) {
	ts, _ := newLookupSetup(b, 16)
	indices, _ := lookupBlockIndices(b, ts.fm, 2)
	for _, enabled := range []bool{false, true} {
		name := "Disabled"
		if enabled {
			name = "Enabled"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := context.Background()
				if enabled {
					ctx, _ = rpcdiag.New(ctx)
				}
				mb := ts.fm.NewMatcherBackend()
				matcher := matcherEnv{ctx: ctx, backend: mb, lastIndex: math.MaxUint64}
				logs, err := matcher.getLogsFromMatches(indices)
				mb.Close()
				if err != nil || len(logs) != len(indices) {
					b.Fatal(err)
				}
			}
		})
	}
}
