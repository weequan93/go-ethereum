// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package filtermaps

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// BenchmarkLogLookup compares the old uncached backend receipt walk with the
// query-local cursor, including matcher registration and its normal index locks.
// These are synthetic warm in-memory lookups, not production RPC percentiles.
func BenchmarkLogLookup(b *testing.B) {
	for _, logsPerReceipt := range []int{1, 4, 16, 128} {
		b.Run(fmt.Sprintf("Logs%d", 2*logsPerReceipt), func(b *testing.B) {
			ts, chain := newLookupSetupBlocks(b, logsPerReceipt, 8)
			dense, _ := lookupBlockIndices(b, ts.fm, 2)
			var across []uint64
			for block := uint64(1); block <= 8; block++ {
				indices, _ := lookupBlockIndices(b, ts.fm, block)
				across = append(across, indices[len(indices)-1])
			}
			for _, query := range []struct {
				name    string
				indices []uint64
			}{
				{"Dense", dense},
				{"Single", dense[:1]},
				{"SparseBlocks", across},
			} {
				b.Run(query.name, func(b *testing.B) {
					for _, cached := range []bool{false, true} {
						name := "Before"
						if cached {
							name = "Cursor"
						}
						b.Run(name, func(b *testing.B) {
							chain.reads.Store(0)
							b.ReportAllocs()
							for b.Loop() {
								mb := ts.fm.NewMatcherBackend()
								// Before runs restore the original three source files using
								// the recorded Go overlay; both versions use the real path.
								m := matcherEnv{ctx: context.Background(), backend: mb, lastIndex: math.MaxUint64}
								logs, err := m.getLogsFromMatches(query.indices)
								if err != nil || len(logs) != len(query.indices) {
									b.Fatalf("candidate lookup failed: %d logs, error %v", len(logs), err)
								}
								mb.Close()
							}
							b.ReportMetric(float64(chain.reads.Load())/float64(b.N), "receipt-reads/query")
						})
					}
				})
			}
		})
	}
}
