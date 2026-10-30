package filtermaps

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/metrics"
)

func writeDiagnosticsView(ts *testSetup, block int) *ChainView {
	return NewChainView(ts.chain, uint64(block), ts.chain.canonical[block])
}

func writeDiagnosticsHash(t *testing.T, db ethdb.KeyValueStore) [32]byte {
	t.Helper()
	h := sha256.New()
	i := db.NewIterator(nil, nil)
	defer i.Release()
	for i.Next() {
		h.Write(i.Key())
		h.Write(i.Value())
	}
	if i.Error() != nil {
		t.Fatal(i.Error())
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// Enabled and disabled writes use the same generated chain and reorg. Compare
// the whole persisted index at every head/range transition, not selected keys.
func TestMapWriteDiagnosticsResultsReorgRollbackAndSafeReplies(t *testing.T) {
	metrics.Enable()
	ts := newTestSetup(t)
	t.Cleanup(ts.close)
	ts.chain.addBlocks(5, 1, 128, 3, false)
	canonical := ts.chain.getCanonicalChain()
	ts.chain.setHead(2)
	ts.chain.addBlocks(3, 1, 128, 3, false)
	fork := ts.chain.getCanonicalChain()
	params := DefaultParams
	params.logMapHeight, params.logValuesPerMap, params.logMapsPerEpoch, params.baseRowGroupSize = 11, 11, 4, 4
	var expected [][32]byte
	for _, enabled := range []bool{false, true} {
		ts.chain.setCanonicalChain(canonical)
		db := rawdb.NewMemoryDatabase()
		f, err := NewFilterMaps(db, writeDiagnosticsView(ts, 1), 0, 0, params, Config{WriteDiagnostics: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.init(); err != nil {
			t.Fatal(err)
		}
		var hashes [][32]byte
		for phase, head := range []int{1, 5, 2, 5} {
			if phase == 2 {
				ts.chain.setCanonicalChain(canonical[:3])
			}
			if phase == 3 {
				ts.chain.setCanonicalChain(fork)
			}
			f.targetView = writeDiagnosticsView(ts, head)
			r, err := f.renderMapsBefore(math.MaxUint32)
			if err != nil || r == nil {
				t.Fatalf("renderer: %v", err)
			}
			r.testDisableWriteDiagnostics = !enabled
			var records []mapWriteRecord
			r.testWriteObserver = func(record mapWriteRecord) {
				if !f.indexLock.TryLock() {
					t.Fatal("observation delivered while indexLock held")
				}
				f.indexLock.Unlock()
				records = append(records, record)
			}
			type result struct {
				value SyncRange
				err   error
			}
			var normal, canceled *FilterMapsMatcherBackend
			var normalResult, canceledResult chan result
			accepted := false
			pause := func() bool {
				if phase != 1 || !f.hasTempRange || accepted {
					return false
				}
				accepted = true
				normal, canceled = f.NewMatcherBackend(), f.NewMatcherBackend()
				normalResult, canceledResult = make(chan result, 1), make(chan result, 1)
				ctx, cancel := context.WithCancel(context.Background())
				go func() { v, e := normal.SyncLogIndex(context.Background()); normalResult <- result{v, e} }()
				go func() { v, e := canceled.SyncLogIndex(ctx); canceledResult <- result{v, e} }()
				// Both requests are accepted during a partial range, yet no reply
				// may be sent until a complete index range is installed.
				f.processSingleEvent(true)
				f.processSingleEvent(true)
				select {
				case <-normalResult:
					t.Fatal("reply escaped temporary range")
				default:
				}
				cancel()
				select {
				case got := <-canceledResult:
					if got.err != context.Canceled {
						t.Fatal("cancellation changed")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancel stuck during partial write")
				}
				return false
			}
			done, err := r.run(pause, func() { f.processSingleEvent(false) })
			if err != nil || !done || f.hasTempRange || !f.indexedRange.headIndexed || f.indexedRange.blocks.Last() != uint64(head) {
				t.Fatalf("inconsistent result at phase%d enabled%v: %v", phase, enabled, err)
			}
			if phase == 1 {
				if !accepted {
					t.Fatal("fixture missed temporary range")
				}
				select {
				case got := <-normalResult:
					if got.err != nil || got.value.IndexedView.HeadNumber() != uint64(head) || got.value.IndexedBlocks != f.indexedRange.blocks {
						t.Fatal("final sync range changed")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("safe reply not released")
				}
				normal.Close()
				canceled.Close()
			}
			if !enabled && len(records) != 0 {
				t.Fatal("disabled observation emitted")
			}
			for _, record := range records {
				if !record.completed || record.uncached == 0 || record.counts[writeRowsVisited] != uint64(f.mapHeight) || record.wall < record.active || record.sequence <= 0 {
					t.Fatalf("invalid detailed record: %+v", record)
				}
				if record.times[writeRowWork] < record.times[writeBaseReadDecode]+record.times[writeEncodeEnqueue] {
					t.Fatal("row-work parent/child timing boundaries")
				}
			}
			if enabled && phase == 1 && len(records) == 0 {
				t.Fatal("uncached map did not produce detail")
			}
			hashes = append(hashes, writeDiagnosticsHash(t, db))
		}
		if !enabled {
			expected = hashes
		} else if !reflect.DeepEqual(hashes, expected) {
			t.Fatal("diagnostics changed persisted index across head/reorg/rollback")
		}
		db.Close()
	}
}

func TestMapWriteDiagnosticsSharedClockBoundaries(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	params := testParams
	params.deriveFields()
	f := &FilterMaps{db: db, Params: params}
	o := &mapWriteObservation{}
	o.reader = mapWriteReader{db: db, observation: o}
	o.writer.observation = o
	batch := db.NewBatch()
	if err := f.storeFilterMapRowsObserved(batch, []uint32{0}, 0, []FilterRow{{1, 2, 3}}, o); err != nil {
		t.Fatal(err)
	}
	if o.counts[writeGroups] != 1 || o.counts[writeDetailClockCalls] != 3 || o.counts[writeHasCalls] != 1 || o.counts[writeGetCalls] != 0 || o.counts[writePutCalls]+o.counts[writeDeleteCalls] != 2 {
		t.Fatalf("unexpected counts: %v", o.counts)
	}
	if err := o.commit(batch); err != nil {
		t.Fatal(err)
	}
	if o.counts[writeDetailClockCalls] != 5 {
		t.Fatal("commit timing did not use exactly two boundaries")
	}
	// Existing rows exercise Has+Get without adding an I/O clock pair.
	if err := f.storeFilterMapRowsObserved(db.NewBatch(), []uint32{0}, 0, []FilterRow{{2, 3, 4}}, o); err != nil {
		t.Fatal(err)
	}
	if o.counts[writeDetailClockCalls] != 8 || o.counts[writeHasCalls] != 2 || o.counts[writeGetCalls] != 1 {
		t.Fatalf("I/O counters changed clock count: %v", o.counts)
	}
}

type writeDiagnosticsFailDB struct {
	ethdb.KeyValueStore
	fail bool
	gets int
}

func (d *writeDiagnosticsFailDB) Has(key []byte) (bool, error) {
	if d.fail {
		return false, errors.New("injected has failure")
	}
	return d.KeyValueStore.Has(key)
}
func (d *writeDiagnosticsFailDB) Get(key []byte) ([]byte, error) {
	d.gets++
	return d.KeyValueStore.Get(key)
}

func TestMapWriteDiagnosticsReadErrorAndRetryParity(t *testing.T) {
	base := rawdb.NewMemoryDatabase()
	defer base.Close()
	db := &writeDiagnosticsFailDB{KeyValueStore: base, fail: true}
	params := testParams
	params.deriveFields()
	f := &FilterMaps{db: db, Params: params}
	observation := &mapWriteObservation{}
	observation.reader = mapWriteReader{db: db, observation: observation}
	observation.writer.observation = observation
	plain := f.storeFilterMapRows(db.NewBatch(), []uint32{0}, 0, []FilterRow{{1, 2, 3}})
	observed := f.storeFilterMapRowsObserved(db.NewBatch(), []uint32{0}, 0, []FilterRow{{1, 2, 3}}, observation)
	if plain == nil || observed == nil || plain.Error() != observed.Error() || db.gets != 0 {
		t.Fatal("read error or DB call order changed")
	}
	db.fail = false
	batch := db.NewBatch()
	if err := f.storeFilterMapRowsObserved(batch, []uint32{0}, 0, []FilterRow{{1, 2, 3}}, observation); err != nil {
		t.Fatal(err)
	}
	if err := observation.commit(batch); err != nil {
		t.Fatal(err)
	}
	rows, err := f.getFilterMapRows([]uint32{0}, 0, false)
	if err != nil || !rows[0].Equal(FilterRow{1, 2, 3}) {
		t.Fatal("successful retry changed")
	}
}

func TestMapWriteDiagnosticsCachedGateAndBoundedQueue(t *testing.T) {
	f := &FilterMaps{filterMapCache: lru.NewCache[uint32, filterMap](3)}
	f.filterMapCache.Add(1, filterMap{FilterRow{1}})
	if newMapWriteObservation(false, f, 1, 2, time.Now()) != nil || newMapWriteObservation(true, f, 1, 2, time.Now()) != nil {
		t.Fatal("cached/disabled path allocated detail")
	}
	q := mapWriteQueue{records: make(chan mapWriteRecord, 1)}
	if !q.offer(mapWriteRecord{}, 1, 0) || q.offer(mapWriteRecord{}, 2, 0) || q.full.Load() != 1 {
		t.Fatal("queue did not drop without blocking")
	}
	q = mapWriteQueue{records: make(chan mapWriteRecord, 1)}
	if !q.offer(mapWriteRecord{}, 1, 30*time.Second) || q.offer(mapWriteRecord{}, 2, 30*time.Second) || q.limited.Load() != 1 {
		t.Fatal("rate limit failed")
	}
}
