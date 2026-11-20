package filtermaps

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/internal/rpcdiag"
)

type syncWaitResult struct {
	rangeResult SyncRange
	err         error
}

func syncWaitFixture() (*FilterMaps, *FilterMapsMatcherBackend) {
	f := &FilterMaps{matcherSyncCh: make(chan *FilterMapsMatcherBackend), disabledCh: make(chan struct{}), indexedView: &ChainView{}}
	return f, &FilterMapsMatcherBackend{f: f}
}

func syncWaitReceive(t *testing.T, result <-chan syncWaitResult) syncWaitResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("sync request did not finish")
		return syncWaitResult{}
	}
}

func syncWaitUntil(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timing phase did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSyncLogIndexWaitDiagnosticsPhaseBoundariesAndResult(t *testing.T) {
	f, fm := syncWaitFixture()
	ctx, trace := rpcdiag.New(context.Background())
	result := make(chan syncWaitResult, 1)
	f.matchersLock.Lock()
	entered := make(chan struct{})
	go func() {
		close(entered)
		r, err := fm.SyncLogIndex(ctx)
		result <- syncWaitResult{r, err}
	}()
	<-entered
	for _, phase := range []rpcdiag.Stage{rpcdiag.SyncRegisterWait, rpcdiag.SyncEnqueueWait, rpcdiag.SyncResponseWait} {
		if trace.Snapshot().Stages[phase].Calls != 0 {
			t.Fatal("phase completed while registration lock held")
		}
	}
	f.matchersLock.Unlock()
	syncWaitUntil(t, func() bool { return trace.Snapshot().Stages[rpcdiag.SyncRegisterWait].Calls == 1 })
	if trace.Snapshot().Stages[rpcdiag.SyncEnqueueWait].Calls != 0 || trace.Snapshot().Stages[rpcdiag.SyncResponseWait].Calls != 0 {
		t.Fatal("later phase completed before request was accepted")
	}
	var request *FilterMapsMatcherBackend
	select {
	case request = <-f.matcherSyncCh:
	case <-time.After(5 * time.Second):
		t.Fatal("request was not enqueued")
	}
	syncWaitUntil(t, func() bool { return trace.Snapshot().Stages[rpcdiag.SyncEnqueueWait].Calls == 1 })
	if trace.Snapshot().Stages[rpcdiag.SyncResponseWait].Calls != 0 {
		t.Fatal("response timer completed without a response")
	}
	want := SyncRange{IndexedView: f.indexedView, ValidBlocks: common.NewRange(uint64(2), uint64(3)), IndexedBlocks: common.NewRange(uint64(4), uint64(5))}
	request.syncCh <- want
	got := syncWaitReceive(t, result)
	if got.err != nil || !reflect.DeepEqual(got.rangeResult, want) {
		t.Fatalf("sync result changed: %+v", got)
	}
	for _, phase := range []rpcdiag.Stage{rpcdiag.SyncRegisterWait, rpcdiag.SyncEnqueueWait, rpcdiag.SyncResponseWait} {
		sample := trace.Snapshot().Stages[phase]
		if sample.Calls != 1 || sample.Nanoseconds <= 0 {
			t.Fatalf("invalid phase sample: %+v", sample)
		}
	}
}

func TestSyncLogIndexWaitDiagnosticsCancelAndDisableParity(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, phase := range []string{"enqueue", "response"} {
			for _, ending := range []string{"cancel", "disabled"} {
				t.Run(phase+"/"+ending+"/"+map[bool]string{true: "traced", false: "plain"}[enabled], func(t *testing.T) {
					f, fm := syncWaitFixture()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var trace *rpcdiag.Trace
					if enabled {
						ctx, trace = rpcdiag.New(ctx)
					}
					finish := func() {
						if ending == "cancel" {
							cancel()
						} else {
							close(f.disabledCh)
						}
					}
					result := make(chan syncWaitResult, 1)
					if phase == "enqueue" {
						finish()
					}
					go func() { value, err := fm.SyncLogIndex(ctx); result <- syncWaitResult{value, err} }()
					if phase == "response" {
						select {
						case <-f.matcherSyncCh:
						case <-time.After(5 * time.Second):
							t.Fatal("request was not accepted")
						}
						finish()
					}
					got := syncWaitReceive(t, result)
					if ending == "cancel" {
						if got.err != context.Canceled || !reflect.DeepEqual(got.rangeResult, SyncRange{}) {
							t.Fatal("cancellation result changed")
						}
					} else if got.err != nil || !reflect.DeepEqual(got.rangeResult, SyncRange{IndexedView: f.indexedView}) {
						t.Fatal("disabled index fallback changed")
					}
					if trace != nil {
						s := trace.Snapshot()
						wantResponse := int64(0)
						if phase == "response" {
							wantResponse = 1
						}
						if s.Stages[rpcdiag.SyncRegisterWait].Calls != 1 || s.Stages[rpcdiag.SyncEnqueueWait].Calls != 1 || s.Stages[rpcdiag.SyncResponseWait].Calls != wantResponse {
							t.Fatal("canceled/disabled phase accounting changed")
						}
					}
				})
			}
		}
	}
}

func TestSyncLogIndexWaitDiagnosticsDeadlineBeforeEnqueue(t *testing.T) {
	_, fm := syncWaitFixture()
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ctx, trace := rpcdiag.New(parent)
	got, err := fm.SyncLogIndex(ctx)
	if err != context.DeadlineExceeded || !reflect.DeepEqual(got, SyncRange{}) {
		t.Fatal("deadline result changed")
	}
	if trace.Snapshot().Stages[rpcdiag.SyncResponseWait].Calls != 0 {
		t.Fatal("response wait recorded without enqueue")
	}
}

func TestSyncLogIndexObservesTemporaryRangeOnCancel(t *testing.T) {
	f, fm := syncWaitFixture()
	f.writeDiagnostics = true
	f.tempRangeTime.set(true)
	defer f.tempRangeTime.set(false)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, trace := rpcdiag.New(parent)
	result := make(chan syncWaitResult, 1)
	go func() {
		value, err := fm.SyncLogIndex(ctx)
		result <- syncWaitResult{value, err}
	}()
	select {
	case <-f.matcherSyncCh:
	case <-time.After(5 * time.Second):
		t.Fatal("sync request did not enqueue")
	}
	syncWaitUntil(t, func() bool { return trace.Snapshot().Stages[rpcdiag.SyncEnqueueWait].Calls == 1 })
	cancel()
	if got := syncWaitReceive(t, result); got.err != context.Canceled {
		t.Fatalf("got %v", got.err)
	}
	s := trace.Snapshot()
	temporary, response := s.Stages[rpcdiag.SyncTempRangeWait], s.Stages[rpcdiag.SyncResponseWait]
	if temporary.Calls != 1 || temporary.Nanoseconds <= 0 || response.Calls != 1 || temporary.Nanoseconds > response.Nanoseconds {
		t.Fatalf("temporary=%+v response=%+v", temporary, response)
	}
}

func TestSyncLogIndexWaitDiagnosticsConcurrentBackendsKeepRequestIsolation(t *testing.T) {
	f, _ := syncWaitFixture()
	const count = 8
	backends := make([]*FilterMapsMatcherBackend, count)
	indices := make(map[*FilterMapsMatcherBackend]int)
	for i := range backends {
		backends[i] = &FilterMapsMatcherBackend{f: f}
		indices[backends[i]] = i
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for i := 0; i < count; i++ {
			request := <-f.matcherSyncCh
			request.syncCh <- SyncRange{ValidBlocks: common.NewRange(uint64(indices[request]), uint64(1))}
		}
	}()
	var wg sync.WaitGroup
	for index, fm := range backends {
		wg.Add(1)
		go func(index int, fm *FilterMapsMatcherBackend) {
			defer wg.Done()
			ctx, trace := rpcdiag.New(context.Background())
			got, err := fm.SyncLogIndex(ctx)
			if err != nil || got.ValidBlocks != common.NewRange(uint64(index), uint64(1)) {
				t.Error("cross-request reply")
			}
			for _, phase := range []rpcdiag.Stage{rpcdiag.SyncRegisterWait, rpcdiag.SyncEnqueueWait, rpcdiag.SyncResponseWait} {
				if trace.Snapshot().Stages[phase].Calls != 1 {
					t.Error("cross-request timing")
				}
			}
		}(index, fm)
	}
	wg.Wait()
	<-serverDone
}
