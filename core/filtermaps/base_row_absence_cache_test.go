package filtermaps

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/metrics"
)

func TestBaseRowAbsenceBounds(t *testing.T) {
	p := DefaultParams
	p.deriveFields()
	var c baseRowAbsenceCache
	c.prepare(0, p)
	if len(c.absent)*8 != 8192 {
		t.Fatal("default bitmap size", len(c.absent)*8)
	}
	c.update(0, 63, nil)
	if !c.knownAbsent(0, 63) || c.knownAbsent(1, 63) || c.knownAbsent(0, p.mapHeight) {
		t.Fatal("key bounds")
	}
	c.prepare(0, p)
	if !c.knownAbsent(0, 63) {
		t.Fatal("warm prepare erased bitmap")
	}
	c.update(0, 63, [][]uint32{nil, {1}})
	if c.knownAbsent(0, 63) {
		t.Fatal("shared positive row marked absent")
	}
	c.update(0, 63, nil)
	c.prepare(32, p)
	if c.knownAbsent(32, 63) {
		t.Fatal("group change retained bit")
	}
	c.update(32, 63, nil)
	p.logMapWidth--
	c.prepare(32, p)
	if c.knownAbsent(32, 63) {
		t.Fatal("parameter change retained bit")
	}
	c.update(32, 63, nil)
	c.invalidate()
	if c.knownAbsent(32, 63) {
		t.Fatal("invalidate retained bit")
	}
	p.mapHeight = baseRowAbsenceMaxBytes*8 + 1
	c.prepare(0, p)
	if len(c.absent) != 0 {
		t.Fatal("bitmap exceeds budget")
	}
}

type absenceDB struct {
	ethdb.KeyValueStore
	has, writes int
	fail        bool
	operations  []string
}

func (d *absenceDB) Has(k []byte) (bool, error) {
	if bytes.HasPrefix(k, []byte("fm-r")) && len(k) == 13 {
		d.has++
		if d.fail {
			return false, errors.New("injected base-row read error")
		}
	}
	return d.KeyValueStore.Has(k)
}
func (d *absenceDB) NewBatch() ethdb.Batch {
	return &absenceBatch{Batch: d.KeyValueStore.NewBatch(), db: d}
}

type absenceBatch struct {
	ethdb.Batch
	db *absenceDB
}

func (b *absenceBatch) Put(k, v []byte) error {
	b.db.operations = append(b.db.operations, "P"+string(k)+string(v))
	return b.Batch.Put(k, v)
}
func (b *absenceBatch) Delete(k []byte) error {
	b.db.operations = append(b.db.operations, "D"+string(k))
	return b.Batch.Delete(k)
}
func (b *absenceBatch) Write() error { b.db.writes++; return b.Batch.Write() }

func TestBaseRowAbsenceSharedRowsAndExtCleanup(t *testing.T) {
	for _, observed := range []bool{false, true} {
		var expectedHashes [][32]byte
		var expectedOps []string
		for _, cached := range []bool{false, true} {
			base := rawdb.NewMemoryDatabase()
			defer base.Close()
			db := &absenceDB{KeyValueStore: base}
			p := testParams
			p.deriveFields()
			f := &FilterMaps{db: db, Params: p}
			if cached {
				f.baseRowAbsence.prepare(0, p)
			}
			long := make(FilterRow, p.baseRowLength+3)
			for i := range long {
				long[i] = uint32(i + 1)
			}
			steps := []struct {
				m   uint32
				row FilterRow
			}{{0, long}, {1, FilterRow{9}}, {0, nil}, {1, nil}, {0, FilterRow{4}}, {1, FilterRow{7}}}
			var hashes [][32]byte
			for n, step := range steps {
				before := db.has
				b := db.NewBatch()
				var err error
				if observed {
					o := &mapWriteObservation{}
					o.reader = mapWriteReader{db: db, observation: o}
					o.writer.observation = o
					err = f.storeFilterMapRowsObserved(b, []uint32{step.m}, 0, []FilterRow{step.row}, o)
				} else {
					err = f.storeFilterMapRows(b, []uint32{step.m}, 0, []FilterRow{step.row})
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = b.Write(); err != nil {
					t.Fatal(err)
				}
				if cached && n == 4 && db.has != before {
					t.Fatal("known empty row still read")
				}
				if cached && n == 5 && db.has != before+1 {
					t.Fatal("positive shared row bypassed DB")
				}
				hashes = append(hashes, writeDiagnosticsHash(t, base))
			}
			rows, err := f.getFilterMapRows([]uint32{0, 1}, 0, false)
			if err != nil || !rows[0].Equal(FilterRow{4}) || !rows[1].Equal(FilterRow{7}) {
				t.Fatal("shared rows or ext cleanup", rows, err)
			}
			if !cached {
				expectedHashes = hashes
				expectedOps = append([]string(nil), db.operations...)
			} else if !reflect.DeepEqual(hashes, expectedHashes) || !reflect.DeepEqual(db.operations, expectedOps) {
				t.Fatal("cache changed persisted rows or ordered mutations")
			}
		}
	}
}

// Force a read failure after at least one row batch has committed. A retry
// rebuilds from the partly persisted range; speculative absence bits must die.
func TestBaseRowAbsencePartialCommitRetry(t *testing.T) {
	metrics.Enable()
	for _, observed := range []bool{false, true} {
		ts := newTestSetup(t)
		defer ts.close()
		ts.chain.addBlocks(5, 1, 128, 3, false)
		var clean [32]byte
		for _, inject := range []bool{false, true} {
			base := rawdb.NewMemoryDatabase()
			defer base.Close()
			db := &absenceDB{KeyValueStore: base}
			p := DefaultParams
			p.logMapHeight = 11
			p.logValuesPerMap = 11
			p.logMapsPerEpoch = 4
			p.baseRowGroupSize = 4
			f, err := NewFilterMaps(db, writeDiagnosticsView(ts, 5), 0, 0, p, Config{WriteDiagnostics: true})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.init(); err != nil {
				t.Fatal(err)
			}
			r, err := f.renderMapsBefore(math.MaxUint32)
			if err != nil || r == nil {
				t.Fatal(err)
			}
			r.testDisableWriteDiagnostics = !observed
			failed := false
			writesAtStart := db.writes
			speculative := false
			done, err := r.run(func() bool {
				if inject && db.writes > writesAtStart {
					for _, word := range f.baseRowAbsence.absent {
						if word != 0 {
							speculative = true
						}
					}
					if speculative {
						db.fail = true
					}
				}
				return false
			}, func() {})
			if inject {
				if err == nil || done || db.writes <= writesAtStart || !speculative {
					t.Fatal("fixture did not fail after batch commit", done, err, db.writes)
				}
				for _, bits := range f.baseRowAbsence.absent {
					if bits != 0 {
						t.Fatal("speculative cache survived failure")
					}
				}
				failed = true
				db.fail = false
				r, err = f.renderMapsBefore(math.MaxUint32)
				if err != nil || r == nil {
					t.Fatal("retry renderer", err)
				}
				r.testDisableWriteDiagnostics = !observed
				done, err = r.run(func() bool { return false }, func() {})
			}
			if err != nil || !done || f.hasTempRange {
				t.Fatal("retry failed", failed, done, err)
			}
			hash := writeDiagnosticsHash(t, base)
			if !inject {
				clean = hash
			} else if hash != clean {
				t.Fatal("partial commit retry differs from clean rebuild")
			}
		}
	}
}
