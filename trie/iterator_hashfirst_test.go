// Copyright 2026, Offchain Labs, Inc.
// For license information, see https://github.com/OffchainLabs/nitro/blob/master/LICENSE.md

package trie

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestHashFirstIterator(t *testing.T) {
	disk := rawdb.NewMemoryDatabase()
	defer disk.Close()
	db := newTestDatabase(disk, rawdb.HashScheme)
	tr := NewEmpty(db)
	for i := 0; i < 256; i++ {
		tr.MustUpdate([]byte{byte(i), 0}, bytes.Repeat([]byte{byte(i)}, 64))
		tr.MustUpdate([]byte{byte(i), 255}, bytes.Repeat([]byte{byte(i + 1)}, 64))
	}
	root, nodes := tr.Commit(false)
	if err := db.Update(root, types.EmptyRootHash, trienode.NewWithNodeSet(nodes)); err != nil {
		t.Fatal(err)
	}
	if err := db.Commit(root); err != nil {
		t.Fatal(err)
	}
	open := func() *Trie {
		tr, err := New(TrieID(root), db)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	for _, bounds := range [][2][]byte{{nil, nil}, {{0}, {128}}, {{128}, nil}, {{12, 0}, {13, 255}}, {{255, 255}, nil}} {
		eager, err := open().NodeIteratorWithRange(bounds[0], bounds[1])
		if err != nil {
			t.Fatal(err)
		}
		lazy, err := open().NodeIteratorWithRangeHashFirst(bounds[0], bounds[1])
		if err != nil {
			t.Fatal(err)
		}
		for eager.Next(true) {
			if !lazy.Next(true) {
				t.Fatalf("early end: %v", lazy.Error())
			}
			if !bytes.Equal(eager.Path(), lazy.Path()) || eager.Hash() != lazy.Hash() || eager.Parent() != lazy.Parent() || eager.Leaf() != lazy.Leaf() {
				t.Fatal("iterator position differs")
			}
			if eager.Leaf() && (!bytes.Equal(eager.LeafKey(), lazy.LeafKey()) || !bytes.Equal(eager.LeafBlob(), lazy.LeafBlob())) {
				t.Fatal("leaf differs")
			}
		}
		if lazy.Next(true) || eager.Error() != nil || lazy.Error() != nil {
			t.Fatalf("iterator error: %v / %v", eager.Error(), lazy.Error())
		}
	}
	// Skipping a hashed child must not invoke the resolver for that child.
	lazy, _ := open().NodeIteratorWithRangeHashFirst(nil, nil)
	reads := 0
	lazy.AddResolver(func(common.Hash, []byte, common.Hash) []byte { reads++; return nil })
	if !lazy.Next(true) || !lazy.Next(true) {
		t.Fatal(lazy.Error())
	}
	if lazy.Hash() == (common.Hash{}) {
		t.Fatal("expected hashed child")
	}
	if reads != 0 {
		t.Fatalf("loaded child before descent: %d", reads)
	}
	missing := lazy.Hash()
	if !lazy.Next(false) || reads != 0 {
		t.Fatalf("skip performed a read: %d, %v", reads, lazy.Error())
	}
	// A missing child is an error on descent, not silently lost from the diff.
	if err := disk.Delete(missing[:]); err != nil {
		t.Fatal(err)
	}
	lazy, _ = open().NodeIteratorWithRangeHashFirst(nil, nil)
	if !lazy.Next(true) || !lazy.Next(true) {
		t.Fatal(lazy.Error())
	}
	if lazy.Hash() != missing {
		t.Fatal("wrong child")
	}
	if lazy.Next(true) || lazy.Error() == nil {
		t.Fatal("missing child was not reported")
	}
	// The same missing child can intentionally be skipped by a hash comparison.
	lazy, _ = open().NodeIteratorWithRangeHashFirst(nil, nil)
	if !lazy.Next(true) || !lazy.Next(true) || !lazy.Next(false) || lazy.Error() != nil {
		t.Fatalf("skip failed: %v", lazy.Error())
	}
}
