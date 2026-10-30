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

package node

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestDatabaseFreezerBatchOptions(t *testing.T) {
	for _, disk := range []bool{false, true} {
		name := "memory"
		if disk {
			name = "disk"
		}
		t.Run(name, func(t *testing.T) {
			config := testNodeConfig()
			if disk {
				config.DataDir = t.TempDir()
			}
			stack, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			defer stack.Close()
			options := DatabaseOptions{Cache: 16, Handles: 16, FreezerMaxBatchBlocks: rawdb.DefaultFreezerMaxBatchBlocks + 1}
			if db, err := stack.OpenDatabaseWithOptions("freezer-test", options); err == nil {
				db.Close()
				t.Fatal("invalid freezer batch limit did not reach rawdb")
			}
			// A rejected open must not leave a database/file lock behind.
			options.FreezerMaxBatchBlocks = 4
			db, err := stack.OpenDatabaseWithOptions("freezer-test", options)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
		})
	}
}
