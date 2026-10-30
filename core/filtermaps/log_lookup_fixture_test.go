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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"sync/atomic"
	"testing"
)

type lookupCountingChain struct {
	blockchain
	reads    atomic.Uint64
	missing  atomic.Bool
	failHash atomic.Pointer[common.Hash]
}

func (c *lookupCountingChain) GetReceiptsByHash(hash common.Hash) types.Receipts {
	c.reads.Add(1)
	failHash := c.failHash.Load()
	if c.missing.Load() || (failHash != nil && *failHash == hash) {
		return nil
	}
	return c.blockchain.GetReceiptsByHash(hash)
}

func newLookupSetup(t testing.TB, logsPerReceipt int) (*testSetup, *lookupCountingChain) {
	return newLookupSetupBlocks(t, logsPerReceipt, 4)
}

func newLookupSetupBlocks(t testing.TB, logsPerReceipt, blocks int) (*testSetup, *lookupCountingChain) {
	t.Helper()
	ts := newTestSetup(t)
	ts.chain.addBlocks(blocks, 2, logsPerReceipt, 4, false)
	chain := &lookupCountingChain{blockchain: ts.chain}
	head := ts.chain.CurrentBlock()
	view := NewChainView(chain, head.Number.Uint64(), head.Hash())
	var err error
	ts.fm, err = NewFilterMaps(ts.db, view, 0, 0, ts.params, Config{})
	if err != nil {
		t.Fatal(err)
	}
	ts.fm.Start()
	ts.fm.WaitIdle()
	t.Cleanup(ts.close)
	return ts, chain
}

func lookupBlockIndices(t testing.TB, f *FilterMaps, block uint64) ([]uint64, []*types.Log) {
	t.Helper()
	f.indexLock.RLock()
	defer f.indexLock.RUnlock()
	index, err := f.getBlockLvPointer(block)
	if err != nil {
		t.Fatal(err)
	}
	var indices []uint64
	var logs []*types.Log
	for _, receipt := range f.indexedView.Receipts(block) {
		for _, log := range receipt.Logs {
			length := uint64(len(log.Topics) + 1)
			remaining := f.valuesPerMap - index%f.valuesPerMap
			if length > remaining {
				index += remaining
			}
			indices, logs = append(indices, index), append(logs, log)
			index += length
		}
	}
	return indices, logs
}
