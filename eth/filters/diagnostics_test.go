package filters

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/filtermaps"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/rpcdiag"
	"github.com/ethereum/go-ethereum/params"
)

func diagnosticFilterSystem(t *testing.T, unindexed bool) (*testBackend, *FilterSystem, common.Address, common.Hash) {
	t.Helper()
	address := common.Address{1}
	genesis := &core.Genesis{Config: params.TestChainConfig, BaseFee: big.NewInt(params.InitialBaseFee)}
	db, blocks, receipts := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 12, func(i int, gen *core.BlockGen) {
		gen.AddUncheckedReceipt(makeReceipt(address))
		gen.AddUncheckedTx(types.NewTransaction(0, address, big.NewInt(0), 21000, gen.BaseFee(), nil))
	})
	t.Cleanup(func() { db.Close() })
	for i, block := range blocks {
		rawdb.WriteBlock(db, block)
		rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
		rawdb.WriteHeadBlockHash(db, block.Hash())
		rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), receipts[i])
	}
	backend, system := newTestFilterSystem(db, Config{})
	backend.startFilterMaps(0, unindexed, filtermaps.DefaultParams)
	t.Cleanup(backend.stopFilterMaps)
	return backend, system, address, blocks[2].Hash()
}

func TestGetLogsDiagnosticsIndexedUnindexedAndHashEquivalence(t *testing.T) {
	for _, unindexed := range []bool{false, true} {
		backend, system, address, hash := diagnosticFilterSystem(t, unindexed)
		api := &FilterAPI{sys: system, events: &EventSystem{backend: backend}}
		for _, criteria := range []FilterCriteria{
			{FromBlock: big.NewInt(1), ToBlock: big.NewInt(8), Addresses: []common.Address{address}},
			{BlockHash: &hash, Addresses: []common.Address{address}},
			{FromBlock: big.NewInt(1), ToBlock: big.NewInt(8), Addresses: []common.Address{{255}}},
			{FromBlock: big.NewInt(8), ToBlock: big.NewInt(1)},
			{BlockHash: &hash, FromBlock: big.NewInt(1)},
		} {
			plain, plainErr := api.GetLogs(context.Background(), criteria)
			ctx, trace := rpcdiag.New(context.Background())
			observed, err := api.GetLogs(ctx, criteria)
			a, _ := json.Marshal(plain)
			b, _ := json.Marshal(observed)
			if string(a) != string(b) || (err == nil) != (plainErr == nil) || err != nil && err.Error() != plainErr.Error() {
				t.Fatalf("result/error changed: %s/%v vs %s/%v", a, plainErr, b, err)
			}
			s := trace.Snapshot()
			if err == nil && s.Fields[rpcdiag.Results] != int64(len(observed)) {
				t.Fatal("result count differs")
			}
			if criteria.BlockHash != nil && s.Fields[rpcdiag.ByHash] != 1 {
				t.Fatal("hash query shape missing")
			}
			if criteria.BlockHash == nil && err == nil {
				if s.Stages[rpcdiag.SyncInitial].Calls != 1 || s.Fields[rpcdiag.ResolvedBlocks] != 8 {
					t.Fatalf("range timing/shape missing: %+v", s)
				}
				if unindexed && s.Fields[rpcdiag.UnindexedBlockVisits] != 8 {
					t.Fatal("unindexed path not measured")
				}
				if !unindexed && s.Stages[rpcdiag.SyncPost].Calls == 0 {
					t.Fatal("post-search index synchronization was bypassed")
				}
			}
		}
	}
}

type diagnosticCanceledBackend struct{ *testBackend }
type diagnosticCanceledMatcher struct{ filtermaps.MatcherBackend }

func (b diagnosticCanceledBackend) NewMatcherBackend() filtermaps.MatcherBackend {
	return diagnosticCanceledMatcher{b.testBackend.NewMatcherBackend()}
}
func (m diagnosticCanceledMatcher) SyncLogIndex(ctx context.Context) (filtermaps.SyncRange, error) {
	<-ctx.Done()
	return filtermaps.SyncRange{}, ctx.Err()
}

func TestGetLogsDiagnosticsPreserveCancellationDuringInitialSync(t *testing.T) {
	backend, _, address, _ := diagnosticFilterSystem(t, false)
	system := NewFilterSystem(diagnosticCanceledBackend{backend}, Config{})
	filter := system.NewRangeFilter(1, 8, []common.Address{address}, nil)
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	for _, enabled := range []bool{false, true} {
		ctx := parent
		var trace *rpcdiag.Trace
		if enabled {
			ctx, trace = rpcdiag.New(ctx)
		}
		logs, err := filter.Logs(ctx)
		if logs != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation result changed")
		}
		if trace != nil && trace.Snapshot().Stages[rpcdiag.SyncInitial].Calls != 1 {
			t.Fatal("canceled synchronization missing from timings")
		}
	}
}
