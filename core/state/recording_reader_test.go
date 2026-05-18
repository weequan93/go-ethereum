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

package state

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/pathdb"
)

type testPreimageRecorder struct {
	entries map[common.Hash][]byte
}

func (r *testPreimageRecorder) RecordPreimage(hash common.Hash, blob []byte) error {
	r.entries[hash] = append([]byte(nil), blob...)
	return nil
}

func TestPathDBForceTrieReadsRecordsPreimages(t *testing.T) {
	trieDB := triedb.NewDatabase(rawdb.NewMemoryDatabase(), &triedb.Config{PathDB: pathdb.Defaults})
	db := NewDatabase(trieDB, nil)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x02")
	value := common.HexToHash("0x03")
	code := []byte{0x60, 0x00, 0x60, 0x00}

	statedb, err := New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatal(err)
	}
	statedb.SetState(addr, slot, value)
	statedb.SetCode(addr, code, tracing.CodeChangeUnspecified)
	root, err := statedb.Commit(1, true, false)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &testPreimageRecorder{entries: make(map[common.Hash][]byte)}
	recordingDB := NewDatabaseWithConfig(trieDB, nil, &CachingDBConfig{
		ForceTrieReads:   true,
		PreimageRecorder: recorder,
	})
	recordingState, err := New(root, recordingDB)
	if err != nil {
		t.Fatal(err)
	}
	if got := recordingState.GetState(addr, slot); got != value {
		t.Fatalf("storage mismatch: have %s want %s", got, value)
	}
	if got := recordingState.GetCode(addr); !bytes.Equal(got, code) {
		t.Fatalf("code mismatch: have %x want %x", got, code)
	}

	codeHash := crypto.Keccak256Hash(code)
	if _, ok := recorder.entries[codeHash]; !ok {
		t.Fatalf("missing recorded code preimage for %s", codeHash)
	}
	if len(recorder.entries) < 2 {
		t.Fatalf("expected trie and code preimages, got %d entries", len(recorder.entries))
	}
	for hash, blob := range recorder.entries {
		if got := crypto.Keccak256Hash(blob); got != hash {
			t.Fatalf("bad preimage for %s: hashes to %s", hash, got)
		}
	}
}

func TestRecordingDBRecordsTrieNodeOrigins(t *testing.T) {
	trieDB := triedb.NewDatabase(rawdb.NewMemoryDatabase(), &triedb.Config{PathDB: pathdb.Defaults})
	recorder := &testPreimageRecorder{entries: make(map[common.Hash][]byte)}
	db := NewDatabaseWithConfig(trieDB, nil, &CachingDBConfig{
		PreimageRecorder: recorder,
	})

	updatedBlob := []byte{0xc2, 0x01, 0x02}
	originBlob := []byte{0xc2, 0x03, 0x04}
	set := trienode.NewNodeSet(common.Hash{})
	set.AddNode([]byte{0x01}, trienode.NewNodeWithPrev(crypto.Keccak256Hash(updatedBlob), updatedBlob, originBlob))

	if err := db.RecordTrieNodePreimages(trienode.NewWithNodeSet(set)); err != nil {
		t.Fatal(err)
	}
	for _, blob := range [][]byte{updatedBlob, originBlob} {
		hash := crypto.Keccak256Hash(blob)
		if got := recorder.entries[hash]; !bytes.Equal(got, blob) {
			t.Fatalf("missing recorded preimage %s: have %x want %x", hash, got, blob)
		}
	}
}

func TestRecordingDBRecordsCommittedTrieNodes(t *testing.T) {
	trieDB := triedb.NewDatabase(rawdb.NewMemoryDatabase(), &triedb.Config{PathDB: pathdb.Defaults})
	recorder := &testPreimageRecorder{entries: make(map[common.Hash][]byte)}
	db := NewDatabaseWithConfig(trieDB, nil, &CachingDBConfig{
		ForceTrieReads:   true,
		PreimageRecorder: recorder,
	})

	statedb, err := New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatal(err)
	}
	addr := common.HexToAddress("0x1234")
	statedb.CreateAccount(addr)
	statedb.SetNonce(addr, 1, tracing.NonceChangeUnspecified)
	statedb.SetState(addr, common.HexToHash("0x02"), common.HexToHash("0x03"))
	if _, err := statedb.Commit(1, true, false); err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) == 0 {
		t.Fatal("expected committed trie nodes to be recorded")
	}
	for hash, blob := range recorder.entries {
		if got := crypto.Keccak256Hash(blob); got != hash {
			t.Fatalf("bad preimage for %s: hashes to %s", hash, got)
		}
	}
}
