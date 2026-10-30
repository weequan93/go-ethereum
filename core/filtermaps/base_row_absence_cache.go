// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package filtermaps

// baseRowAbsenceMaxBytes bounds the retained bitmap even with non-default
// parameters. DefaultParams uses 8192 bytes, one bit for each of 65536 rows.
const baseRowAbsenceMaxBytes = 64 * 1024

// baseRowAbsenceCache remembers base rows whose combined group is empty, for
// one head group only. It is owned by the indexer; matchers always read the DB.
// All row writers update it, including writers outside the head rendering path.
// Mutations may precede batch commit while writing a finished map, so a failed
// write must discard the bitmap before it can be reused by another write.
type baseRowAbsenceCache struct {
	group  uint32
	params Params
	absent []uint64
}

func (c *baseRowAbsenceCache) prepare(group uint32, params Params) {
	words := (uint64(params.mapHeight) + 63) / 64
	if words == 0 || words > baseRowAbsenceMaxBytes/8 {
		*c = baseRowAbsenceCache{}
		return
	}
	if c.group == group && c.params == params && len(c.absent) == int(words) {
		return
	}
	if len(c.absent) != int(words) {
		c.absent = make([]uint64, int(words))
	} else {
		clear(c.absent)
	}
	c.group, c.params = group, params
}

func (c *baseRowAbsenceCache) invalidate() {
	clear(c.absent)
}

func (c *baseRowAbsenceCache) knownAbsent(group, row uint32) bool {
	return len(c.absent) != 0 && c.group == group && row < c.params.mapHeight &&
		c.absent[row/64]&(uint64(1)<<(row%64)) != 0
}

// update must inspect the entire combined group, not just the map being
// replaced: another map's base row prevents caching this group row as absent.
// No slices or mutable rendered-map data are retained.
func (c *baseRowAbsenceCache) update(group, row uint32, rows [][]uint32) {
	if len(c.absent) == 0 || c.group != group || row >= c.params.mapHeight {
		return
	}
	word, bit := row/64, uint64(1)<<(row%64)
	c.absent[word] &^= bit
	for _, values := range rows {
		if len(values) != 0 {
			return
		}
	}
	c.absent[word] |= bit
}
