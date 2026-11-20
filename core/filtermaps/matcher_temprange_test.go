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
	"math"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestMatcherDuringHeadMapRewrite checks searches that run while the head map
// is being rewritten. writeFinishedMaps applies a temporary range whose blocks
// end at L = lastBlockOfMap(headMap-1), so block L-1 is the last fully indexed
// block and stays in the matchers' valid range. A pointer request for a block
// beyond the range must point after block L-1, otherwise the logs of L-1 are
// silently excluded from a search that is later considered valid.
func TestMatcherDuringHeadMapRewrite(t *testing.T) {
	ts := newTestSetup(t)
	defer ts.close()

	ts.chain.addBlocks(200, 5, 3, 4, false) // every block has logs
	ts.setHistory(0, false)
	ts.fm.WaitIdle()
	f := ts.fm
	// Stop the indexer goroutine (it owns the range fields) so that this test
	// can apply the temporary range itself; the database stays open.
	f.Stop()
	ts.fm = nil

	// Apply the temporary range exactly as writeFinishedMaps does when it
	// re-renders the current head map (head rendering: renderBefore=MaxUint32).
	f.indexLock.Lock()
	orig := f.indexedRange
	headMap := orig.maps.Last()
	r := &mapRenderer{f: f, finished: common.NewRange(headMap, 0), renderBefore: math.MaxUint32}
	temp, err := r.getTempRange()
	if err != nil {
		f.indexLock.Unlock()
		t.Fatalf("getTempRange failed: %v", err)
	}
	f.setRange(f.db, f.indexedView, temp, true)
	f.indexLock.Unlock()
	defer func() {
		f.indexLock.Lock()
		f.setRange(f.db, f.indexedView, orig, false)
		f.indexLock.Unlock()
	}()

	if temp.headIndexed || temp.blocks.Count() < 2 {
		t.Fatalf("unexpected temporary range: blocks %v headIndexed %v", temp.blocks, temp.headIndexed)
	}
	afterLast := temp.blocks.AfterLast() // L (probably partially rendered)
	lastFull := temp.blocks.Last()       // L-1 (fully indexed, still valid)

	mb := f.NewMatcherBackend()
	defer mb.Close()
	ctx := context.Background()

	want, err := f.getBlockLvPointer(afterLast)
	if err != nil {
		t.Fatalf("failed to read pointer of block %d: %v", afterLast, err)
	}
	got, err := mb.GetBlockLvPointer(ctx, orig.blocks.AfterLast()+10)
	if err != nil {
		t.Fatalf("GetBlockLvPointer failed: %v", err)
	}
	if got != want {
		startOfLastFull, _ := f.getBlockLvPointer(lastFull)
		t.Fatalf("pointer of future block during head map rewrite: got %d (start of block %d = %d), want %d (after block %d)",
			got, lastFull, startOfLastFull, want, lastFull)
	}

	// The logs of the last fully indexed block must be found by a search whose
	// end is beyond the temporary range.
	receipts := ts.chain.GetReceiptsByHash(ts.chain.GetCanonicalHash(lastFull))
	if len(receipts) == 0 || len(receipts[0].Logs) == 0 {
		t.Fatalf("block %d has no logs", lastFull)
	}
	target := receipts[0].Logs[0]
	logs, err := GetPotentialMatches(ctx, mb, lastFull, afterLast, []common.Address{target.Address}, nil)
	if err != nil {
		t.Fatalf("GetPotentialMatches failed: %v", err)
	}
	for _, log := range logs {
		if log.Address == target.Address {
			return
		}
	}
	t.Fatalf("log of fully indexed block %d not found by search [%d, %d] during head map rewrite (%d results)", lastFull, lastFull, afterLast, len(logs))
}
