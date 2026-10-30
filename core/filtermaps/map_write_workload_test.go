package filtermaps

// This measurement runs the unchanged renderer against an in-memory index DB.
// It is copied into an isolated source tree, never into the production checkout.
import (
	"bytes"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/metrics"
)

type mapWriteWorkload struct {
	DatabaseSHA256   string  `json:"database_sha256"`
	MutationSHA256   string  `json:"mutation_sha256"`
	Name             string  `json:"phase"`
	MapsBefore       uint32  `json:"maps_before"`
	MapsAfter        uint32  `json:"maps_after"`
	BaseHas          uint64  `json:"base_row_has"`
	BaseGet          uint64  `json:"base_row_get"`
	BasePut          uint64  `json:"base_row_put"`
	BaseDelete       uint64  `json:"base_row_delete"`
	ExtPut           uint64  `json:"extended_row_put"`
	ExtDelete        uint64  `json:"extended_row_delete"`
	BatchWrites      uint64  `json:"batch_writes"`
	EmptyBatchWrites uint64  `json:"empty_batch_writes"`
	PutValueBytes    uint64  `json:"put_value_bytes"`
	ReadMS           float64 `json:"base_has_get_wall_ms"`
	BatchMS          float64 `json:"batch_write_wall_ms"`
	RenderRunMS      float64 `json:"render_run_wall_ms"`
	ActiveWriteMS    float64 `json:"active_map_write_sample_max_ms"`
	WriteSamples     int64   `json:"active_map_write_sample_count"`
	Callbacks        uint64  `json:"renderer_callbacks"`
}

type mapWriteCountingDB struct {
	ethdb.KeyValueStore
	stats     *mapWriteWorkload
	mutations hash.Hash
}

func baseRowKey(key []byte) bool {
	return len(key) == len("fm-r")+9 && bytes.HasPrefix(key, []byte("fm-r"))
}

func (db *mapWriteCountingDB) Has(key []byte) (bool, error) {
	if db.stats == nil || !baseRowKey(key) {
		return db.KeyValueStore.Has(key)
	}
	db.stats.BaseHas++
	start := time.Now()
	value, err := db.KeyValueStore.Has(key)
	db.stats.ReadMS += float64(time.Since(start)) / float64(time.Millisecond)
	return value, err
}

func (db *mapWriteCountingDB) Get(key []byte) ([]byte, error) {
	if db.stats == nil || !baseRowKey(key) {
		return db.KeyValueStore.Get(key)
	}
	db.stats.BaseGet++
	start := time.Now()
	value, err := db.KeyValueStore.Get(key)
	db.stats.ReadMS += float64(time.Since(start)) / float64(time.Millisecond)
	return value, err
}

func (db *mapWriteCountingDB) NewBatch() ethdb.Batch {
	return &mapWriteCountingBatch{Batch: db.KeyValueStore.NewBatch(), db: db}
}

type mapWriteCountingBatch struct {
	ethdb.Batch
	db  *mapWriteCountingDB
	ops int
}

func (b *mapWriteCountingBatch) recordMutation(kind byte, key, value []byte) {
	if b.db.mutations == nil {
		return
	}
	var size [8]byte
	b.db.mutations.Write([]byte{kind})
	binary.LittleEndian.PutUint64(size[:], uint64(len(key)))
	b.db.mutations.Write(size[:])
	b.db.mutations.Write(key)
	binary.LittleEndian.PutUint64(size[:], uint64(len(value)))
	b.db.mutations.Write(size[:])
	b.db.mutations.Write(value)
}
func (b *mapWriteCountingBatch) Put(key, value []byte) error {
	b.recordMutation('P', key, value)
	b.ops++
	if s := b.db.stats; s != nil {
		s.PutValueBytes += uint64(len(value))
		if bytes.HasPrefix(key, []byte("fm-r")) {
			if baseRowKey(key) {
				s.BasePut++
			} else {
				s.ExtPut++
			}
		}
	}
	return b.Batch.Put(key, value)
}

func (b *mapWriteCountingBatch) Delete(key []byte) error {
	b.recordMutation('D', key, nil)
	b.ops++
	if s := b.db.stats; s != nil && bytes.HasPrefix(key, []byte("fm-r")) {
		if baseRowKey(key) {
			s.BaseDelete++
		} else {
			s.ExtDelete++
		}
	}
	return b.Batch.Delete(key)
}

func (b *mapWriteCountingBatch) Write() error {
	if b.db.stats == nil {
		return b.Batch.Write()
	}
	b.db.stats.BatchWrites++
	if b.ops == 0 {
		b.db.stats.EmptyBatchWrites++
	}
	start := time.Now()
	err := b.Batch.Write()
	b.db.stats.BatchMS += float64(time.Since(start)) / float64(time.Millisecond)
	return err
}

// TestMapWriteWorkloadMeasurement measures a genuine map boundary transition
// and checks every stored row against the rendered map, including the previous
// map sharing the same base-row group. The fixtures contain generated receipts;
// no latency or exact operation-count assertion is used as a correctness test.
func TestMapWriteWorkloadMeasurement(t *testing.T) {
	// Only this isolated test uses a deterministic receipt generator; production
	// crypto randomness is untouched. The package runs this fixture serially.
	savedRandom := crand.Reader
	crand.Reader = rand.New(rand.NewSource(20261107))
	defer func() { crand.Reader = savedRandom }()
	metrics.Enable()
	ts := newTestSetup(t)
	t.Cleanup(ts.close)
	ts.chain.addBlocks(1, 1, 1024, 3, false)
	head := ts.chain.CurrentBlock()
	view := NewChainView(ts.chain, head.Number.Uint64(), head.Hash())
	db := &mapWriteCountingDB{KeyValueStore: ts.db}
	f, err := NewFilterMaps(db, view, 0, 0, DefaultParams, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.init(); err != nil {
		t.Fatal(err)
	}
	// No asynchronous indexer is started. ts.fm stays nil so chain updates
	// cannot send events to an unstarted indexer.
	originalTimer := mapWriteTimer
	defer func() { mapWriteTimer = originalTimer }()
	for _, phase := range []struct {
		name         string
		blocks, logs int
	}{
		{"first_uncached_map", 0, 0},
		{"cached_head_append", 1, 1},
		{"cross_into_next_uncached_map", 15, 1024},
	} {
		if phase.blocks > 0 {
			ts.chain.addBlocks(phase.blocks, 1, phase.logs, 3, false)
		}
		head := ts.chain.CurrentBlock()
		f.targetView = NewChainView(ts.chain, head.Number.Uint64(), head.Hash())
		renderer, err := f.renderMapsBefore(math.MaxUint32)
		if err != nil || renderer == nil {
			t.Fatalf("renderer: %v", err)
		}
		stats := &mapWriteWorkload{Name: phase.name, MapsBefore: f.indexedRange.maps.Count()}
		mapWriteTimer = metrics.NewTimer()
		db.stats = stats
		db.mutations = sha256.New()
		start := time.Now()
		done, err := renderer.run(func() bool { stats.Callbacks++; return false }, func() {})
		stats.RenderRunMS = float64(time.Since(start)) / float64(time.Millisecond)
		db.stats = nil // verification reads are deliberately excluded
		stats.MutationSHA256 = hex.EncodeToString(db.mutations.Sum(nil))
		db.mutations = nil
		digest := writeDiagnosticsHash(t, ts.db)
		stats.DatabaseSHA256 = hex.EncodeToString(digest[:])
		timer := mapWriteTimer.Snapshot()
		stats.ActiveWriteMS = float64(timer.Max()) / float64(time.Millisecond)
		stats.WriteSamples = timer.Count()
		mapWriteTimer.Stop()
		if err != nil || !done {
			t.Fatalf("render: done=%v err=%v", done, err)
		}
		stats.MapsAfter = f.indexedRange.maps.Count()
		if !f.indexedRange.headIndexed || f.hasTempRange || f.indexedRange.blocks.Last() != head.Number.Uint64() {
			t.Fatal("renderer did not install the complete head range")
		}
		// Read storage directly, bypassing filterMapCache.
		for mapIndex := range f.indexedRange.maps.Iter() {
			expected, ok := f.filterMapCache.Get(mapIndex)
			if !ok {
				t.Fatalf("no rendered reference for map %d", mapIndex)
			}
			for rowIndex := uint32(0); rowIndex < f.mapHeight; rowIndex++ {
				stored, err := f.getFilterMapRows([]uint32{mapIndex}, rowIndex, false)
				if err != nil || !stored[0].Equal(expected[rowIndex]) {
					t.Fatalf("map %d row %d readback differs: %v", mapIndex, rowIndex, err)
				}
			}
		}
		document, err := json.Marshal(stats)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("MAP_WRITE_WORKLOAD %s", document)
	}
	if f.indexedRange.maps.Count() < 2 {
		t.Fatal("fixture did not cross a map boundary")
	}
}
