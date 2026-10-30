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

package rawdb

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
)

func TestChainFreezerBatchOptions(t *testing.T) {
	for _, limit := range []uint64{0, 1, 256, DefaultFreezerMaxBatchBlocks, DefaultFreezerMaxBatchBlocks + 1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			kv := memorydb.New()
			defer kv.Close()
			db, err := Open(kv, OpenOptions{ReadOnly: true, FreezerMaxBatchBlocks: limit})
			if limit > DefaultFreezerMaxBatchBlocks {
				if err == nil {
					db.Close()
					t.Fatal("invalid batch limit accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			want := limit
			if want == 0 {
				want = DefaultFreezerMaxBatchBlocks
			}
			if got := db.(*freezerdb).maxBatchBlocks; got != want {
				t.Fatalf("batch limit = %d, want %d", got, want)
			}
		})
	}
}

// Record the ancient count at hot-database cleanup boundaries. Each distinct
// count is a committed freezer batch, with the writer lock already released.
type batchTrackingDatabase struct {
	ethdb.KeyValueStore
	freezer *chainFreezer
	counts  []uint64
}

func (db *batchTrackingDatabase) NewBatch() ethdb.Batch {
	return &batchTrackingBatch{Batch: db.KeyValueStore.NewBatch(), db: db}
}

type batchTrackingBatch struct {
	ethdb.Batch
	db *batchTrackingDatabase
}

func (b *batchTrackingBatch) Write() error {
	if err := b.Batch.Write(); err != nil {
		return err
	}
	// A reader must be able to acquire the freezer lock between chunks.
	return b.db.freezer.ReadAncients(func(reader ethdb.AncientReaderOp) error {
		count, err := reader.Ancients()
		if err == nil && (len(b.db.counts) == 0 || b.db.counts[len(b.db.counts)-1] != count) {
			b.db.counts = append(b.db.counts, count)
		}
		return err
	})
}

// Full batches must continue without the one-minute backoff, including when
// the eligible range is an exact multiple of the configured batch limit.
func TestChainFreezerSmallBatches(t *testing.T) {
	for _, tc := range []struct {
		blocks      uint64
		counts      []uint64
		missingBody uint64
	}{
		{3, []uint64{3}, 0},
		{11, []uint64{4, 8, 11}, 0},
		{12, []uint64{4, 8, 12}, 0},
		{11, []uint64{4, 8, 11}, 5},
	} {
		t.Run(fmt.Sprintf("%d/missing-%d", tc.blocks, tc.missingBody), func(t *testing.T) {
			kv := memorydb.New()
			defer kv.Close()
			var parent common.Hash
			headers := make([]*types.Header, tc.blocks)
			for number := uint64(0); number < tc.blocks; number++ {
				header := &types.Header{Number: new(big.Int).SetUint64(number), ParentHash: parent, Difficulty: new(big.Int)}
				parent = header.Hash()
				headers[number] = header
				WriteHeader(kv, header)
				WriteCanonicalHash(kv, parent, number)
				if number != tc.missingBody || tc.missingBody == 0 {
					WriteBody(kv, parent, number, &types.Body{})
				}
				WriteReceipts(kv, parent, number, types.Receipts{})
			}
			WriteHeadBlockHash(kv, parent)
			WriteFinalizedBlockHash(kv, parent)

			dir := t.TempDir()
			freezer, err := newChainFreezer(dir, "", "", false, 4)
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			t.Cleanup(func() {
				if !closed {
					freezer.Close()
				}
			})
			db := &batchTrackingDatabase{KeyValueStore: kv, freezer: freezer}
			freezer.wg.Add(1)
			go func() {
				defer freezer.wg.Done()
				freezer.freeze(db)
			}()

			// The manual trigger acknowledges a completed cycle and avoids
			// sleeping for the production recheck interval.
			freezeCycle := func() {
				t.Helper()
				trigger := make(chan struct{}, 1)
				select {
				case freezer.trigger <- trigger:
				case <-time.After(10 * time.Second):
					t.Fatal("freezer did not finish all chunks before backing off")
				}
				select {
				case <-trigger:
				case <-time.After(10 * time.Second):
					t.Fatal("freeze cycle did not finish")
				}
			}
			freezeCycle()
			if tc.missingBody != 0 {
				// The first chunk remains committed; the failed second chunk
				// must roll back and leave its hot records available for retry.
				if !reflect.DeepEqual(db.counts, []uint64{4}) {
					t.Fatalf("batch boundaries before recovery = %v, want [4]", db.counts)
				}
				if count, _ := freezer.Ancients(); count != 4 {
					t.Fatalf("failed batch not rolled back: ancient count = %d", count)
				}
				if value, _ := kv.Get(headerHashKey(4)); len(value) == 0 {
					t.Fatal("failed batch deleted its hot records")
				}
				WriteBody(kv, headers[tc.missingBody].Hash(), tc.missingBody, &types.Body{})
				freezeCycle()
			}
			if !reflect.DeepEqual(db.counts, tc.counts) {
				t.Fatalf("committed batch boundaries = %v, want %v", db.counts, tc.counts)
			}
			if err := freezer.Close(); err != nil {
				t.Fatal(err)
			}
			closed = true

			// Reopening verifies that all tables were synced before their
			// canonical hot-database records were deleted.
			reopened, err := newChainFreezer(dir, "", "", true, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			reader := &freezerdb{KeyValueStore: kv, chainFreezer: reopened}
			for number, header := range headers {
				n := uint64(number)
				hash := header.Hash()
				if n != 0 {
					if value, _ := kv.Get(headerHashKey(n)); len(value) != 0 {
						t.Fatalf("canonical hash %d remains in hot database", n)
					}
				}
				if got := ReadCanonicalHash(reader, n); got != hash {
					t.Fatalf("canonical hash %d = %s, want %s", n, got, hash)
				}
				if got := ReadHeader(reader, hash, n); got == nil || got.Hash() != hash {
					t.Fatalf("header %d not preserved", n)
				}
				if ReadBody(reader, hash, n) == nil || len(ReadReceiptsRLP(reader, hash, n)) == 0 {
					t.Fatalf("body or receipts %d not preserved", n)
				}
			}
		})
	}
}
