package filtermaps

import (
	"math"
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
)

// Same fixture and benchmark code is used before/after via a Go source overlay.
// CachedSameHead exercises the entire default65536row loop without detail.
// UncachedSameHead measures observation overhead, not a performance benefit.
func BenchmarkMapWriteDiagnostics(b *testing.B) {
	metrics.Enable()
	for _, uncached := range []bool{false, true} {
		name := "CachedSameHead"
		if uncached {
			name = "UncachedSameHead"
		}
		b.Run(name, func(b *testing.B) {
			ts := newTestSetup(b)
			defer ts.close()
			ts.chain.addBlocks(1, 1, 16, 3, false)
			head := ts.chain.CurrentBlock()
			f, err := NewFilterMaps(ts.db, NewChainView(ts.chain, head.Number.Uint64(), head.Hash()), 0, 0, DefaultParams, Config{WriteDiagnostics: true})
			if err != nil {
				b.Fatal(err)
			}
			if err := f.init(); err != nil {
				b.Fatal(err)
			}
			r, err := f.renderMapsBefore(math.MaxUint32)
			if err != nil {
				b.Fatal(err)
			}
			if done, err := r.run(func() bool { return false }, func() {}); err != nil || !done {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if uncached {
					f.filterMapCache.Purge()
				}
				r, err := f.renderMapsBefore(math.MaxUint32)
				if err != nil || r == nil {
					b.Fatal(err)
				}
				if done, err := r.run(func() bool { return false }, func() {}); err != nil || !done {
					b.Fatal(err)
				}
			}
		})
	}
}
