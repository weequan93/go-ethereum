package rpcdiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	publicrpcdiag "github.com/ethereum/go-ethereum/rpcdiag"
)

func TestTraceContextPreservesCancellationDeadlineAndValues(t *testing.T) {
	type key struct{}
	parent, cancel := context.WithDeadline(context.WithValue(context.Background(), key{}, "keep"), time.Now().Add(time.Hour))
	observed, trace := New(parent)
	if FromContext(observed) != trace || observed.Value(key{}) != "keep" || observed.Done() != parent.Done() {
		t.Fatal("parent context semantics changed")
	}
	want, _ := parent.Deadline()
	got, _ := observed.Deadline()
	if !got.Equal(want) {
		t.Fatal("deadline changed")
	}
	cancel()
	if !errors.Is(observed.Err(), context.Canceled) {
		t.Fatal("cancellation changed")
	}
	if FromContext(context.Background()) != nil {
		t.Fatal("trace leaked between requests")
	}
}

func TestTraceConcurrentTotalsMaximaAndIsolation(t *testing.T) {
	_, trace := New(context.Background())
	var wg sync.WaitGroup
	for i := 1; i <= 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				trace.AddTime(ReceiptsRead, time.Duration(i))
				trace.Add(ReceiptReads, 1)
				trace.Max(ResolvedBlocks, int64(i))
			}
		}(i)
	}
	wg.Wait()
	s := trace.Snapshot()
	if s.Stages[ReceiptsRead] != (Timing{Nanoseconds: 13600, Calls: 1600, MaxNanoseconds: 16}) || s.Fields[ReceiptReads] != 1600 || s.Fields[ResolvedBlocks] != 16 {
		t.Fatalf("lost concurrent metadata: %+v", s)
	}
	_, other := New(context.Background())
	if other.Snapshot().Stages[ReceiptsRead].Calls != 0 {
		t.Fatal("cross-request state")
	}
	// Nested/parallel operation totals are allowed to exceed wall time.
	trace.AddTime(IndexLockWait, time.Hour)
	if trace.Snapshot().Stages[IndexLockWait].Nanoseconds != int64(time.Hour) {
		t.Fatal("nested work incorrectly clamped")
	}
}

func TestTraceNilAndNegativeSamplesAreNoOps(t *testing.T) {
	var trace *Trace
	if !trace.Start().IsZero() {
		t.Fatal("disabled timer read the clock")
	}
	trace.End(Params, time.Time{})
	trace.Add(Results, 1)
	trace.Max(ResolvedBlocks, 1)
	trace.Error(context.Canceled)
	var lock sync.RWMutex
	held := trace.RLock(&lock)
	trace.RUnlock(&lock, held)
	_, trace = New(context.Background())
	trace.AddTime(Params, -1)
	trace.Add(Results, -1)
	if trace.Snapshot().Stages[Params].Calls != 0 || trace.Snapshot().Fields[Results] != 0 {
		t.Fatal("negative data accepted")
	}
}

func TestNamedTransactionStagesBridgeThroughPublicContext(t *testing.T) {
	ctx, trace := New(context.Background())
	finish := publicrpcdiag.StartNamed(ctx, "tx_publish_queue_wait")
	finish()
	if got := trace.Snapshot().Stages[TxPublishQueueWait].Calls; got != 1 {
		t.Fatalf("queue wait calls=%d", got)
	}
	if got := trace.Snapshot().Stages[TxPublishQueueWait].Nanoseconds; got < 0 {
		t.Fatalf("queue wait duration=%d", got)
	}
	publicrpcdiag.StartNamed(context.Background(), "tx_publish_queue_wait")()
	if got := trace.Snapshot().Stages[TxPublishQueueWait].Calls; got != 1 {
		t.Fatalf("unrelated context changed trace calls=%d", got)
	}
}

func TestTransactionQueueObservationPreservesSequenceIDInSlowLog(t *testing.T) {
	ctx, trace := New(context.Background())
	recorder := publicrpcdiag.FromContext(ctx).(publicrpcdiag.TxQueueRecorder)
	recorder.RecordTxQueue(publicrpcdiag.TxQueueObservation{
		Dequeued: true, FirstDequeue: 20 * time.Millisecond,
		NonceWait: 10 * time.Millisecond, SequenceWait: 500 * time.Millisecond, LastSeqID: 71,
	})
	snapshot := trace.Snapshot()
	for stage, want := range map[Stage]time.Duration{
		TxPublishFirstDequeue: 20 * time.Millisecond, TxPublishNonceWait: 10 * time.Millisecond,
		TxPublishSequenceWait: 500 * time.Millisecond,
	} {
		if got := snapshot.Stages[stage]; got.Nanoseconds != int64(want) || got.Calls != 1 {
			t.Fatalf("stage %d got %+v", stage, got)
		}
	}
	logFields := snapshot.LogFields()
	for i := 0; i+1 < len(logFields); i += 2 {
		if logFields[i] == "tx_publish_last_seq_id" && logFields[i+1] == uint64(71) {
			return
		}
	}
	t.Fatal("slow log missing sequencing operation ID")
}

func TestTraceErrorClassesAndBoundedFields(t *testing.T) {
	for _, entry := range []struct {
		err  error
		kind int64
	}{
		{errors.New("private-error-value"), FailureOther}, {fmt.Errorf("wrapped: %w", context.Canceled), FailureOther}, {context.Canceled, FailureCanceled}, {context.DeadlineExceeded, FailureDeadline},
	} {
		_, trace := New(context.Background())
		trace.Error(entry.err)
		if trace.Snapshot().Fields[FailureKind] != entry.kind {
			t.Fatal("wrong error category")
		}
		for _, field := range trace.Snapshot().LogFields() {
			if reflect.DeepEqual(field, "private-error-value") {
				t.Fatal("error payload retained")
			}
		}
	}
	for _, name := range stageNames {
		if name == "" {
			t.Fatal("unnamed stage")
		}
	}
	for _, name := range fieldNames {
		if name == "" {
			t.Fatal("unnamed counter")
		}
	}
}

func TestTraceReadLockKeepsMutualExclusion(t *testing.T) {
	_, trace := New(context.Background())
	var lock sync.RWMutex
	held := trace.RLock(&lock)
	if lock.TryLock() {
		t.Fatal("read lock bypassed")
	}
	trace.RUnlock(&lock, held)
	if !lock.TryLock() {
		t.Fatal("read lock not released")
	}
	lock.Unlock()
	s := trace.Snapshot()
	if s.Stages[IndexLockWait].Calls != 1 || s.Stages[IndexLockHold].Calls != 1 {
		t.Fatal("lock timing missing")
	}
}

func TestRequestParamsAreRenderedInSlowDetailFields(t *testing.T) {
	_, trace := New(context.Background())
	trace.SetRequestParams([]byte(`[{"fromBlock":"0x10","toBlock":"latest","address":"0x01","topics":[null,"0x02"]}]`))
	encoded, err := json.Marshal(trace.Snapshot().LogFields())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{"request_params_json", "fromBlock", "toBlock", "latest", "request_params_sha256", "request_params_truncated"} {
		if !strings.Contains(text, want) {
			t.Fatalf("request params missing %q: %s", want, text)
		}
	}
}

func BenchmarkTraceOverhead(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "Disabled"
		if enabled {
			name = "Enabled"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := context.Background()
				var trace *Trace
				if enabled {
					ctx, trace = New(ctx)
				}
				trace = FromContext(ctx)
				for _, stage := range []Stage{Resolve, SyncInitial, IndexedSearch, SyncPost} {
					start := trace.Start()
					trace.End(stage, start)
				}
				trace.Add(Results, 3)
			}
		})
	}
}
