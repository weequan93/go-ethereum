package catalyst

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
)

func blobProviderTestBundle() (common.Hash, *engine.BlobsBundle, common.Hash) {
	var commitment kzg4844.Commitment
	commitment[0] = 0x55
	blob := make(hexutil.Bytes, len(kzg4844.Blob{}))
	blob[0], blob[len(blob)-1] = 0x42, 0x23
	return common.HexToHash("0x1234"), &engine.BlobsBundle{
		Commitments: []hexutil.Bytes{commitment[:]},
		Blobs:       []hexutil.Bytes{blob},
	}, commitmentToVersionedHash(commitment)
}

func TestSimulatedBeaconBlobProviderConcurrentMining(t *testing.T) {
	ctx := context.Background()
	genesis := core.DeveloperGenesisBlock(10_000_000, nil)
	node, service, beacon := startSimulatedBeaconEthService(t, genesis, 0)
	defer node.Close()
	if err := beacon.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	hash, bundle, versionedHash := blobProviderTestBundle()
	beacon.blobsBundleProvider[hash] = bundle

	start, done := make(chan struct{}), make(chan struct{})
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for {
			if err := beacon.Initialize(ctx); err != nil {
				errors <- err
				return
			}
			select {
			case <-done:
				return
			default:
				runtime.Gosched()
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for {
			blobs, err := beacon.GetBlobs(ctx, hash, []common.Hash{versionedHash})
			if err != nil {
				errors <- err
				return
			}
			if len(blobs) != 1 || !bytes.Equal(blobs[0][:], bundle.Blobs[0]) {
				errors <- fmt.Errorf("blob changed during concurrent initialization/mining")
				return
			}
			select {
			case <-done:
				return
			default:
				runtime.Gosched()
			}
		}
	}()
	defer func() {
		close(done)
		workers.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
	}()
	close(start)
	for range 6 {
		before := service.BlockChain().CurrentBlock().Number.Uint64()
		blockHash := beacon.Commit()
		if after := service.BlockChain().CurrentBlock().Number.Uint64(); after != before+1 {
			t.Fatalf("mining did not advance the head: before=%d after=%d", before, after)
		}
		// Even an empty post-Cancun bundle is published through sealBlock's
		// map-write path, concurrently with initialization and reads above.
		if _, err := beacon.GetBlobs(ctx, blockHash, nil); err != nil {
			t.Fatalf("mined block bundle was not retained: %v", err)
		}
	}
}

func TestSimulatedBeaconBlobProviderInitializePreservesBundles(t *testing.T) {
	ctx := context.Background()
	beacon := new(SimulatedBeacon)
	if err := beacon.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	hash, bundle, versionedHash := blobProviderTestBundle()
	beacon.blobsBundleProvider[hash] = bundle
	// Multiple consensus nodes can use the same simulated L1 beacon as their
	// blob reader. Starting another reader must not erase existing sidecars.
	for range 3 {
		if err := beacon.Initialize(ctx); err != nil {
			t.Fatal(err)
		}
		blobs, err := beacon.GetBlobs(ctx, hash, []common.Hash{versionedHash})
		if err != nil {
			t.Fatalf("reinitialization lost a previously available blob: %v", err)
		}
		if len(blobs) != 1 || !bytes.Equal(blobs[0][:], bundle.Blobs[0]) {
			t.Fatal("blob contents changed during repeated initialization")
		}
	}
}
