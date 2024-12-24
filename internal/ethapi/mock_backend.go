package ethapi

import (
	"context"
	"errors"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	arbitrum_core "github.com/ethereum/go-ethereum/arbitrum-core"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/bloombits"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"math/big"
	"testing"
	"time"
)

func newMockAccountManager(t *testing.T) (*accounts.Manager, accounts.Account) {
	var (
		dir        = t.TempDir()
		am         = accounts.NewManager(&accounts.Config{InsecureUnlockAllowed: true})
		b          = keystore.NewKeyStore(dir, 2, 1)
		testKey, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	)
	acc, err := b.ImportECDSA(testKey, "")
	if err != nil {
		t.Fatalf("failed to create test account: %v", err)
	}
	if err := b.Unlock(acc, ""); err != nil {
		t.Fatalf("failed to unlock account: %v\n", err)
	}
	am.AddBackend(b)
	return am, acc
}

type TestBackend struct {
	db      ethdb.Database
	chain   *core.BlockChain
	pending *types.Block
	accman  *accounts.Manager
	acc     accounts.Account
}

func NewTestBackend(t *testing.T, n int, gspec *core.Genesis, engine consensus.Engine, generator func(i int, b *core.BlockGen)) *TestBackend {
	var (
		cacheConfig = &core.CacheConfig{
			TrieCleanLimit:    256,
			TrieDirtyLimit:    256,
			TrieTimeLimit:     5 * time.Minute,
			SnapshotLimit:     0,
			TrieDirtyDisabled: true, // Archive mode
		}
	)
	accman, acc := newMockAccountManager(t)
	gspec.Alloc[acc.Address] = types.Account{Balance: big.NewInt(params.Ether)}
	// Generate blocks for testing
	db, blocks, _ := core.GenerateChainWithGenesis(gspec, engine, n, generator)
	txlookupLimit := uint64(0)
	chain, err := core.NewBlockChain(db, cacheConfig, nil, gspec, nil, engine, vm.Config{}, nil, &txlookupLimit)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	if n, err := chain.InsertChain(blocks); err != nil {
		t.Fatalf("block %d: failed to insert into chain: %v", n, err)
	}

	backend := &TestBackend{db: db, chain: chain, accman: accman, acc: acc}
	return backend
}

func (b *TestBackend) setPendingBlock(block *types.Block) {
	b.pending = block
}

func (b TestBackend) SyncProgress() ethereum.SyncProgress { return ethereum.SyncProgress{} }
func (b TestBackend) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (b TestBackend) FeeHistory(ctx context.Context, blockCount uint64, lastBlock rpc.BlockNumber, rewardPercentiles []float64) (*big.Int, [][]*big.Int, []*big.Int, []float64, error) {
	return nil, nil, nil, nil, nil
}
func (b TestBackend) ChainDb() ethdb.Database           { return b.db }
func (b TestBackend) AccountManager() *accounts.Manager { return b.accman }
func (b TestBackend) ExtRPCEnabled() bool               { return false }
func (b TestBackend) RPCGasCap() uint64                 { return 10000000 }
func (b TestBackend) RPCEVMTimeout() time.Duration      { return time.Second }
func (b TestBackend) RPCTxFeeCap() float64              { return 0 }
func (b TestBackend) UnprotectedAllowed() bool          { return false }
func (b TestBackend) SetHead(number uint64)             {}
func (b TestBackend) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error) {
	if number == rpc.LatestBlockNumber {
		return b.chain.CurrentBlock(), nil
	}
	if number == rpc.PendingBlockNumber && b.pending != nil {
		return b.pending.Header(), nil
	}
	return b.chain.GetHeaderByNumber(uint64(number)), nil
}
func (b TestBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return b.chain.GetHeaderByHash(hash), nil
}
func (b TestBackend) ArbStateByBlockNumber(ctx context.Context, number rpc.BlockNumber) (*arbitrum_core.ArbState, error) {

	var state *state.StateDB

	header, err := b.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, errors.New("header not found")
	}

	stateDb, err := b.chain.StateAt(header.Root)
	if err != nil {
		return nil, err
	}
	state = stateDb

	return arbitrum_core.New(state), nil
}

func (b TestBackend) HeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.HeaderByNumber(ctx, blockNr)
	}
	if blockHash, ok := blockNrOrHash.Hash(); ok {
		return b.HeaderByHash(ctx, blockHash)
	}
	panic("unknown type rpc.BlockNumberOrHash")
}
func (b TestBackend) CurrentHeader() *types.Header { return b.chain.CurrentBlock() }
func (b TestBackend) CurrentBlock() *types.Header  { return b.chain.CurrentBlock() }
func (b TestBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	if number == rpc.LatestBlockNumber {
		head := b.chain.CurrentBlock()
		return b.chain.GetBlock(head.Hash(), head.Number.Uint64()), nil
	}
	if number == rpc.PendingBlockNumber {
		return b.pending, nil
	}
	return b.chain.GetBlockByNumber(uint64(number)), nil
}
func (b TestBackend) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return b.chain.GetBlockByHash(hash), nil
}
func (b TestBackend) BlockByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Block, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.BlockByNumber(ctx, blockNr)
	}
	if blockHash, ok := blockNrOrHash.Hash(); ok {
		return b.BlockByHash(ctx, blockHash)
	}
	panic("unknown type rpc.BlockNumberOrHash")
}
func (b TestBackend) GetBody(ctx context.Context, hash common.Hash, number rpc.BlockNumber) (*types.Body, error) {
	return b.chain.GetBlock(hash, uint64(number.Int64())).Body(), nil
}
func (b TestBackend) StateAndHeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	if number == rpc.PendingBlockNumber {
		panic("pending state not implemented")
	}
	header, err := b.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, nil, err
	}
	if header == nil {
		return nil, nil, errors.New("header not found")
	}
	stateDb, err := b.chain.StateAt(header.Root)
	return stateDb, header, err
}
func (b TestBackend) StateAndHeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*state.StateDB, *types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.StateAndHeaderByNumber(ctx, blockNr)
	}
	panic("only implemented for number")
}
func (b TestBackend) PendingBlockAndReceipts() (*types.Block, types.Receipts) { panic("implement me") }
func (b TestBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	header, err := b.HeaderByHash(ctx, hash)
	if header == nil || err != nil {
		return nil, err
	}
	receipts := rawdb.ReadReceipts(b.db, hash, header.Number.Uint64(), header.Time, b.chain.Config())
	return receipts, nil
}
func (b TestBackend) GetTd(ctx context.Context, hash common.Hash) *big.Int {
	if b.pending != nil && hash == b.pending.Hash() {
		return nil
	}
	return big.NewInt(1)
}
func (b TestBackend) GetEVM(ctx context.Context, msg *core.Message, state *state.StateDB, header *types.Header, vmConfig *vm.Config, blockContext *vm.BlockContext) *vm.EVM {
	if vmConfig == nil {
		vmConfig = b.chain.GetVMConfig()
	}
	txContext := core.NewEVMTxContext(msg)
	context := core.NewEVMBlockContext(header, b.chain, nil)
	if blockContext != nil {
		context = *blockContext
	}
	return vm.NewEVM(context, txContext, state, b.chain.Config(), *vmConfig)
}
func (b TestBackend) SubscribeChainEvent(ch chan<- core.ChainEvent) event.Subscription {
	panic("implement me")
}
func (b TestBackend) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription {
	panic("implement me")
}
func (b TestBackend) SubscribeChainSideEvent(ch chan<- core.ChainSideEvent) event.Subscription {
	panic("implement me")
}
func (b TestBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	panic("implement me")
}
func (b TestBackend) GetTransaction(ctx context.Context, txHash common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64, error) {
	tx, blockHash, blockNumber, index := rawdb.ReadTransaction(b.db, txHash)
	return true, tx, blockHash, blockNumber, index, nil
}
func (b TestBackend) GetPoolTransactions() (types.Transactions, error)         { panic("implement me") }
func (b TestBackend) GetPoolTransaction(txHash common.Hash) *types.Transaction { panic("implement me") }
func (b TestBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return 0, nil
}
func (b TestBackend) Stats() (pending int, queued int) { panic("implement me") }
func (b TestBackend) TxPoolContent() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) {
	panic("implement me")
}
func (b TestBackend) TxPoolContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) {
	panic("implement me")
}
func (b TestBackend) SubscribeNewTxsEvent(events chan<- core.NewTxsEvent) event.Subscription {
	panic("implement me")
}
func (b TestBackend) ChainConfig() *params.ChainConfig { return b.chain.Config() }
func (b TestBackend) Engine() consensus.Engine         { return b.chain.Engine() }
func (b TestBackend) GetLogs(ctx context.Context, blockHash common.Hash, number uint64) ([][]*types.Log, error) {
	panic("implement me")
}
func (b TestBackend) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) event.Subscription {
	panic("implement me")
}
func (b TestBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	panic("implement me")
}
func (b TestBackend) SubscribePendingLogsEvent(ch chan<- []*types.Log) event.Subscription {
	panic("implement me")
}
func (b TestBackend) BloomStatus() (uint64, uint64) { panic("implement me") }
func (b TestBackend) ServiceFilter(ctx context.Context, session *bloombits.MatcherSession) {
	panic("implement me")
}

func (b TestBackend) FallbackClient() types.FallbackClient {
	return nil
}

func (b TestBackend) SyncProgressMap() map[string]interface{} {
	return map[string]interface{}{}
}
