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
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// referenceLogLookup is the receipt walk used before the query-local cursor.
func referenceLogLookup(receipts types.Receipts, first, valuesPerMap, target uint64) *types.Log {
	for _, receipt := range receipts {
		for _, log := range receipt.Logs {
			length := uint64(len(log.Topics) + 1)
			remaining := valuesPerMap - first%valuesPerMap
			if length > remaining {
				first += remaining
			}
			if first > target {
				return nil
			}
			if first == target {
				return log
			}
			first += length
		}
	}
	return nil
}

func lookupReceipts(shape []byte) types.Receipts {
	receipts := make(types.Receipts, 1, len(shape)+1)
	receipts[0] = &types.Receipt{}
	for i, count := range shape {
		receipt := &types.Receipt{}
		for j := byte(0); j < count%9; j++ {
			receipt.Logs = append(receipt.Logs, &types.Log{
				Address: common.BytesToAddress([]byte{byte(i), j}), Topics: make([]common.Hash, (int(count)+int(j))%5),
			})
		}
		receipts = append(receipts, receipt)
	}
	return receipts
}

func TestLogLookupCursorEquivalence(t *testing.T) {
	receipts := lookupReceipts([]byte{0, 8, 1, 7, 0, 3, 5, 2, 4})
	before := make(types.Receipts, len(receipts))
	for i, receipt := range receipts {
		copyReceipt := *receipt
		before[i] = &copyReceipt
		if receipt.Logs != nil {
			before[i].Logs = make([]*types.Log, len(receipt.Logs))
		}
		for j, log := range receipt.Logs {
			copy := *log
			copy.Topics = append([]common.Hash{}, log.Topics...)
			before[i].Logs[j] = &copy
		}
	}
	for _, order := range []string{"ascending", "descending", "shuffled", "duplicate"} {
		t.Run(order, func(t *testing.T) {
			indices := make([]uint64, 256)
			for i := range indices {
				indices[i] = uint64(i)
			}
			switch order {
			case "descending":
				for i := range indices {
					indices[i] = uint64(len(indices) - i - 1)
				}
			case "shuffled":
				rand.New(rand.NewSource(1)).Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
			case "duplicate":
				for i := range indices {
					indices[i] = uint64(i / 2)
				}
			}
			cursor := logLookupCursor{receipts: receipts, firstIndex: 29}
			for _, index := range indices {
				if got, want := cursor.find(32, index), referenceLogLookup(receipts, 29, 32, index); got != want {
					t.Fatalf("index %d: got %p, want %p", index, got, want)
				}
			}
		})
	}
	if !reflect.DeepEqual(receipts, before) {
		t.Fatal("lookup changed receipt/log contents")
	}
}

func FuzzLogLookupCursor(f *testing.F) {
	f.Add([]byte{0, 1, 8, 2, 7}, byte(29), []byte{0, 29, 31, 32, 50, 50, 40, 255})
	f.Add([]byte{}, byte(0), []byte{0, 1, 255})
	f.Fuzz(func(t *testing.T, shape []byte, first byte, queries []byte) {
		if len(shape) > 64 || len(queries) > 256 {
			return
		}
		receipts := lookupReceipts(shape)
		cursor := logLookupCursor{receipts: receipts, firstIndex: uint64(first)}
		for _, query := range queries {
			if got, want := cursor.find(32, uint64(query)), referenceLogLookup(receipts, uint64(first), 32, uint64(query)); got != want {
				t.Fatalf("query %d: got %p, want %p", query, got, want)
			}
		}
	})
}

func TestLogLookupBackendReadsOncePerBlock(t *testing.T) {
	ts, chain := newLookupSetup(t, 16)
	indices, expected := lookupBlockIndices(t, ts.fm, 2)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	var cache logLookupCache
	chain.reads.Store(0)
	for i, index := range indices {
		got, err := mb.getLogByLvIndexCached(index, &cache)
		if err != nil || got != expected[i] {
			t.Fatalf("cached lookup %d: got %p, want %p, error %v", index, got, expected[i], err)
		}
	}
	if got := chain.reads.Load(); got != 1 {
		t.Fatalf("cached query read receipts %d times, want 1", got)
	}
	chain.reads.Store(0)
	for i, index := range indices {
		ts.fm.indexLock.RLock()
		got, err := ts.fm.getLogByLvIndex(index)
		ts.fm.indexLock.RUnlock()
		if err != nil || got != expected[i] {
			t.Fatalf("uncached lookup %d differs: %v", index, err)
		}
	}
	if got := chain.reads.Load(); got != uint64(len(indices)) {
		t.Fatalf("uncached query read receipts %d times, want %d", got, len(indices))
	}
	other, _ := lookupBlockIndices(t, ts.fm, 3)
	chain.reads.Store(0)
	if _, err := mb.getLogByLvIndexCached(other[0], &cache); err != nil {
		t.Fatal(err)
	}
	if _, err := mb.getLogByLvIndexCached(indices[0], &cache); err != nil {
		t.Fatal(err)
	}
	if got := chain.reads.Load(); got != 2 {
		t.Fatalf("only the current block should be retained; got %d reads", got)
	}
}

func TestLogLookupBackendRetriesReceiptError(t *testing.T) {
	ts, chain := newLookupSetup(t, 4)
	indices, expected := lookupBlockIndices(t, ts.fm, 2)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	var cache logLookupCache
	chain.reads.Store(0)
	chain.missing.Store(true)
	if got, err := mb.getLogByLvIndexCached(indices[0], &cache); err == nil || got != nil {
		t.Fatalf("missing receipts: got %p, error %v", got, err)
	}
	chain.missing.Store(false)
	if got, err := mb.getLogByLvIndexCached(indices[0], &cache); err != nil || got != expected[0] {
		t.Fatalf("retry: got %p, error %v", got, err)
	}
	if got := chain.reads.Load(); got != 2 {
		t.Fatalf("failed receipt read must not be cached, got %d reads", got)
	}
}

func TestLogLookupBackendReorgAndHeadTruncation(t *testing.T) {
	ts, _ := newLookupSetup(t, 8)
	indices, expected := lookupBlockIndices(t, ts.fm, 3)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	var cache logLookupCache
	if got, err := mb.getLogByLvIndexCached(indices[0], &cache); err != nil || got != expected[0] {
		t.Fatal("initial lookup failed", err)
	}
	ts.chain.setHead(2)
	ts.chain.addBlocks(2, 2, 8, 4, false)
	ts.fm.WaitIdle()
	newIndices, replacement := lookupBlockIndices(t, ts.fm, 3)
	if replacement[0] == expected[0] {
		t.Fatal("test did not replace the block")
	}
	if got, err := mb.getLogByLvIndexCached(newIndices[0], &cache); err != nil || got != replacement[0] {
		t.Fatalf("returned stale reorg data: got %p, want %p, error %v", got, replacement[0], err)
	}
	ts.chain.setHead(1)
	ts.fm.WaitIdle()
	for _, index := range newIndices {
		ts.fm.indexLock.RLock()
		want, wantErr := ts.fm.getLogByLvIndex(index)
		ts.fm.indexLock.RUnlock()
		got, err := mb.getLogByLvIndexCached(index, &cache)
		if got != want || (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
			t.Fatalf("trimmed index %d: got %p/%v, want %p/%v", index, got, err, want, wantErr)
		}
	}
}

func TestLogLookupCacheRequiresExactSnapshot(t *testing.T) {
	view := &ChainView{}
	indexRange := filterMapsRange{initialized: true, headIndexed: true, headDelimiter: 7,
		maps: common.NewRange[uint32](1, 2), blocks: common.NewRange[uint64](3, 4)}
	cache := logLookupCache{view: view, indexedRange: indexRange, blockNumber: 4,
		cursor: logLookupCursor{receipts: types.Receipts{}}}
	if !cache.matches(view, indexRange, 4) {
		t.Fatal("exact snapshot should be reusable")
	}
	changed := indexRange
	changed.headDelimiter++
	if cache.matches(view, changed, 4) || cache.matches(&ChainView{}, indexRange, 4) || cache.matches(view, indexRange, 5) {
		t.Fatal("reused a different view, index snapshot or block")
	}
}

func TestLogLookupBackendConcurrentQueries(t *testing.T) {
	ts, _ := newLookupSetup(t, 16)
	indices, expected := lookupBlockIndices(t, ts.fm, 2)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			var localCache logLookupCache
			for i := range indices {
				at := (i*7 + worker) % len(indices)
				got, err := mb.getLogByLvIndexCached(indices[at], &localCache)
				if err != nil || got != expected[at] {
					t.Errorf("concurrent lookup: got %p, want %p, error %v", got, expected[at], err)
				}
			}
		}(worker)
	}
	group.Wait()
}

// This wrapper exposes only the public interface and therefore takes the
// original path. It provides an independent fallback comparison for the batch.
type lookupPlainBackend struct{ MatcherBackend }

func TestLogLookupCandidateBatchEquivalence(t *testing.T) {
	ts, chain := newLookupSetup(t, 16)
	indices, _ := lookupBlockIndices(t, ts.fm, 2)
	mb := ts.fm.NewMatcherBackend()
	defer mb.Close()
	for _, order := range []string{"ordered", "shuffled", "duplicate", "bounded", "false_positives", "out_of_range", "single", "empty"} {
		t.Run(order, func(t *testing.T) {
			matches := append(potentialMatches(nil), indices...)
			first, last := indices[0], indices[len(indices)-1]
			switch order {
			case "shuffled":
				rand.New(rand.NewSource(7)).Shuffle(len(matches), func(i, j int) { matches[i], matches[j] = matches[j], matches[i] })
			case "duplicate":
				matches = append(matches, indices...)
			case "bounded":
				first, last = indices[3], indices[len(indices)-4]
			case "false_positives":
				matches = nil
				for index := first; index <= last; index++ {
					matches = append(matches, index)
				}
			case "out_of_range":
				first = last + 1
			case "single":
				matches = matches[:1]
			case "empty":
				matches = nil
			}
			plain := matcherEnv{ctx: context.Background(), backend: lookupPlainBackend{mb}, firstIndex: first, lastIndex: last}
			cached := matcherEnv{ctx: context.Background(), backend: mb, firstIndex: first, lastIndex: last}
			want, wantErr := plain.getLogsFromMatches(matches)
			got, err := cached.getLogsFromMatches(matches)
			if err != wantErr || !reflect.DeepEqual(got, want) {
				t.Fatalf("batch differs from fallback: got %v/%v, want %v/%v", got, err, want, wantErr)
			}
		})
	}
	// Batch errors preserve the public path's error text and partial-result shape.
	chain.missing.Store(true)
	plain := matcherEnv{ctx: context.Background(), backend: lookupPlainBackend{mb}, lastIndex: ^uint64(0)}
	cached := matcherEnv{ctx: context.Background(), backend: mb, lastIndex: ^uint64(0)}
	want, wantErr := plain.getLogsFromMatches(indices)
	got, err := cached.getLogsFromMatches(indices)
	if !reflect.DeepEqual(got, want) || err == nil || wantErr == nil || err.Error() != wantErr.Error() {
		t.Fatalf("batch error differs: got %v/%v, want %v/%v", got, err, want, wantErr)
	}
	chain.missing.Store(false)
	other, _ := lookupBlockIndices(t, ts.fm, 3)
	hash := ts.chain.GetCanonicalHash(3)
	chain.failHash.Store(&hash)
	matches := append(append(potentialMatches(nil), indices...), other...)
	want, wantErr = plain.getLogsFromMatches(matches)
	got, err = cached.getLogsFromMatches(matches)
	if len(got) != len(indices) || !reflect.DeepEqual(got, want) || err == nil || wantErr == nil || err.Error() != wantErr.Error() {
		t.Fatalf("partial batch differs: got %d/%v, want %d/%v", len(got), err, len(want), wantErr)
	}
}

func TestLogLookupConcurrentIndexUpdates(t *testing.T) {
	ts, _ := newLookupSetup(t, 16)
	indices, _ := lookupBlockIndices(t, ts.fm, 2)
	canonical := ts.chain.getCanonicalChain()
	stop := make(chan struct{})
	var group sync.WaitGroup
	var checked atomic.Uint64
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			var cache logLookupCache
			for {
				for _, index := range indices {
					select {
					case <-stop:
						return
					default:
					}
					ts.fm.indexLock.RLock()
					want, wantErr := ts.fm.getLogByLvIndex(index)
					got, err := ts.fm.getLogByLvIndexCached(index, &cache)
					ts.fm.indexLock.RUnlock()
					if got != want || (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
						t.Errorf("index changed: got %p/%v, want %p/%v", got, err, want, wantErr)
						return
					}
					checked.Add(1)
				}
			}
		}()
	}
	for _, head := range []int{1, 4, 2, 4, 1, 3, 4, 2, 4} {
		ts.chain.setCanonicalChain(canonical[:head+1])
		ts.fm.WaitIdle()
	}
	close(stop)
	group.Wait()
	if checked.Load() == 0 {
		t.Fatal("concurrent reader did not run")
	}
}
