package filtermaps

import (
	"github.com/ethereum/go-ethereum/core/rawdb"
	"math"
	"testing"
)

// A fresh DB and renderer on every iteration exercises a genuinely cold head
// group, including the first bitmap allocation. Shared with the V2 baseline.
func BenchmarkBaseRowAbsenceColdHead(b *testing.B) {
	ts := newTestSetup(b)
	defer ts.close()
	ts.chain.addBlocks(1, 1, 16, 3, false)
	head := ts.chain.CurrentBlock()
	view := NewChainView(ts.chain, head.Number.Uint64(), head.Hash())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := rawdb.NewMemoryDatabase()
		f, err := NewFilterMaps(db, view, 0, 0, DefaultParams, Config{})
		if err != nil {
			b.Fatal(err)
		}
		if err = f.init(); err != nil {
			b.Fatal(err)
		}
		r, err := f.renderMapsBefore(math.MaxUint32)
		if err != nil || r == nil {
			b.Fatal(err)
		}
		if done, err := r.run(func() bool { return false }, func() {}); err != nil || !done {
			b.Fatal(err)
		}
		db.Close()
	}
}
