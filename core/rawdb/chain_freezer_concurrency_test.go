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
	"bytes"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
)

func TestChainFreezerReadAncientsConcurrentReaders(t *testing.T) {
	store, _ := newFreezerForTesting(t, freezerTestTableDef)
	defer store.Close()
	freezer := &chainFreezer{ancients: store}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var readers sync.WaitGroup
	defer func() {
		close(release)
		readers.Wait()
	}()
	startReader := func() {
		readers.Add(1)
		go func() {
			defer readers.Done()
			if err := freezer.ReadAncients(func(reader ethdb.AncientReaderOp) error {
				entered <- struct{}{}
				<-release
				_, err := reader.Ancients()
				return err
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	startReader()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first reader did not enter")
	}
	startReader()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("a read-only ancient callback blocked another reader")
	}
	if store.writeLock.TryLock() {
		store.writeLock.Unlock()
		t.Fatal("concurrent readers did not exclude mutations")
	}
}

func TestChainFreezerReadAncientsMutationExclusion(t *testing.T) {
	for _, mutation := range []string{"append", "truncate-head", "truncate-tail", "close"} {
		t.Run(mutation, func(t *testing.T) {
			store, _ := newFreezerForTesting(t, freezerTestTableDef)
			defer store.Close()
			freezer := &chainFreezer{ancients: store}
			if _, err := freezer.ModifyAncients(func(writer ethdb.AncientWriteOp) error {
				for i := uint64(0); i < 2; i++ {
					if err := writer.AppendRaw("test", i, []byte{byte(i)}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			readDone, writeDone := make(chan error, 1), make(chan error, 1)
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			go func() {
				readDone <- freezer.ReadAncients(func(reader ethdb.AncientReaderOp) error {
					close(entered)
					<-release
					count, err := reader.Ancients()
					if err != nil || count != 2 {
						return fmt.Errorf("ancient count changed within read callback: %d, %v", count, err)
					}
					tail, err := reader.Tail()
					if err != nil || tail != 0 {
						return fmt.Errorf("ancient tail changed within read callback: %d, %v", tail, err)
					}
					value, err := reader.Ancient("test", 0)
					if err != nil || !bytes.Equal(value, []byte{0}) {
						return fmt.Errorf("ancient data changed within read callback: %x, %v", value, err)
					}
					return nil
				})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not enter")
			}
			if store.writeLock.TryLock() {
				store.writeLock.Unlock()
				t.Fatal("read callback did not exclude mutations")
			}
			go func() {
				var err error
				switch mutation {
				case "append":
					_, err = freezer.ModifyAncients(func(writer ethdb.AncientWriteOp) error {
						return writer.AppendRaw("test", 2, []byte{2})
					})
				case "truncate-head":
					_, err = freezer.TruncateHead(1)
				case "truncate-tail":
					_, err = freezer.TruncateTail(1)
				case "close":
					err = store.Close()
				}
				writeDone <- err
			}()
			unlock()
			for _, done := range []chan error{readDone, writeDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("reader or mutation did not finish")
				}
			}
		})
	}
}

func TestChainFreezerConcurrentReadAndFreeze(t *testing.T) {
	const blocks = 64
	kv := memorydb.New()
	defer kv.Close()
	var parent common.Hash
	headers := make([]*types.Header, blocks)
	for n := range headers {
		header := &types.Header{Number: big.NewInt(int64(n)), ParentHash: parent, Difficulty: new(big.Int)}
		parent = header.Hash()
		headers[n] = header
		WriteHeader(kv, header)
		WriteCanonicalHash(kv, parent, uint64(n))
		WriteBody(kv, parent, uint64(n), &types.Body{})
		WriteReceipts(kv, parent, uint64(n), types.Receipts{})
	}
	WriteHeadBlockHash(kv, parent)
	WriteFinalizedBlockHash(kv, parent)
	freezer, err := newChainFreezer(t.TempDir(), "", "", false, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer freezer.Close()
	db := &freezerdb{KeyValueStore: kv, chainFreezer: freezer}
	start := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func(offset int) {
			defer readers.Done()
			<-start
			for step := 0; step < 256; step++ {
				n := uint64((step + offset) % blocks)
				want := headers[n].Hash()
				if hash := ReadCanonicalHash(db, n); hash != want {
					t.Errorf("canonical hash %d changed during freezing: %s, want %s", n, hash, want)
					return
				}
				if header := ReadHeader(db, want, n); header == nil || header.Hash() != want {
					t.Errorf("header %d unavailable during freezing", n)
					return
				}
				if ReadBody(db, want, n) == nil || len(ReadReceiptsRLP(db, want, n)) == 0 {
					t.Errorf("body or receipts %d unavailable during freezing", n)
					return
				}
			}
		}(i)
	}
	close(start)
	defer readers.Wait()
	freezer.wg.Add(1)
	go func() {
		defer freezer.wg.Done()
		freezer.freeze(kv)
	}()
	trigger := make(chan struct{}, 1)
	select {
	case freezer.trigger <- trigger:
	case <-time.After(10 * time.Second):
		t.Fatal("freezing did not finish")
	}
	select {
	case <-trigger:
	case <-time.After(10 * time.Second):
		t.Fatal("freeze cycle did not acknowledge completion")
	}
}

// Model canonical-hash/header lookups that fall back to the hot database, as
// unindexed log scans and chain-history reads do. This is a synthetic benchmark,
// not an end-to-end RPC latency or disk-I/O benchmark.
func BenchmarkChainFreezerHotHeaderReads(b *testing.B) {
	const blocks = 2048
	kv := memorydb.New()
	defer kv.Close()
	var parent common.Hash
	for n := uint64(0); n < blocks; n++ {
		header := &types.Header{Number: new(big.Int).SetUint64(n), ParentHash: parent, Difficulty: new(big.Int)}
		parent = header.Hash()
		WriteHeader(kv, header)
		WriteCanonicalHash(kv, parent, n)
	}
	freezer, err := newChainFreezer(b.TempDir(), "", "", false, 256)
	if err != nil {
		b.Fatal(err)
	}
	defer freezer.Close()
	db := &freezerdb{KeyValueStore: kv, chainFreezer: freezer}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var n uint64
		for pb.Next() {
			hash := ReadCanonicalHash(db, n)
			if header := ReadHeader(db, hash, n); header == nil || header.Hash() != hash {
				b.Error("canonical header lookup failed")
				return
			}
			n = (n + 1) % blocks
		}
	})
}
