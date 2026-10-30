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

import "github.com/ethereum/go-ethereum/core/types"

// logLookupCache retains only the current block for one candidate batch. Both the chain
// view and the complete index range must still match before reusing receipts.
// No block-number-only result is reused across a reorg or index update.
type logLookupCache struct {
	view         *ChainView
	indexedRange filterMapsRange
	blockNumber  uint64
	cursor       logLookupCursor
}

func (c *logLookupCache) matches(view *ChainView, indexedRange filterMapsRange, block uint64) bool {
	return c.cursor.receipts != nil && c.view == view && c.indexedRange == indexedRange && c.blockNumber == block
}

// logLookupCursor scans receipts incrementally for increasing candidate indices.
// It keeps no additional per-log index and restarts for out-of-order lookups.
// The receipts and returned logs remain read-only and retain their original order.
type logLookupCursor struct {
	receipts   types.Receipts
	firstIndex uint64
	lastQuery  uint64
	started    bool
	receipt    int
	log        int
	index      uint64
}

func (c *logLookupCursor) find(valuesPerMap, target uint64) *types.Log {
	if !c.started || target < c.lastQuery {
		c.receipt, c.log, c.index = 0, 0, c.firstIndex
	}
	c.started, c.lastQuery = true, target
	for c.receipt < len(c.receipts) {
		logs := c.receipts[c.receipt].Logs
		if c.log == len(logs) {
			c.receipt++
			c.log = 0
			continue
		}
		log := logs[c.log]
		length := uint64(len(log.Topics) + 1)
		remaining := valuesPerMap - c.index%valuesPerMap
		if length > remaining {
			c.index += remaining // skip padding at the map boundary
		}
		if c.index > target {
			return nil // a topic or padding value, rather than a log address
		}
		if c.index == target {
			return log
		}
		c.index += length
		c.log++
	}
	return nil
}
