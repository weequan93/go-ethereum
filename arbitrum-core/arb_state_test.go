package arbitrum_core

import (
	"context"
	"crypto/ecdsa"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

type account struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

var (
	acc1Key, _ = crypto.HexToECDSA("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
	acc2Key, _ = crypto.HexToECDSA("49a7b37aa6f6645917e7b807e9d1c00d4fa71f18343b0d4122a4d2df64dd6fee")
	acc1Addr   = crypto.PubkeyToAddress(acc1Key.PublicKey)
	acc2Addr   = crypto.PubkeyToAddress(acc2Key.PublicKey)
)

func newRPCBytes(bytes []byte) *hexutil.Bytes {
	rpcBytes := hexutil.Bytes(bytes)
	return &rpcBytes
}

func newAccounts(n int) (accounts []account) {
	for i := 0; i < n; i++ {
		key, _ := crypto.GenerateKey()
		addr := crypto.PubkeyToAddress(key.PublicKey)
		accounts = append(accounts, account{key: key, addr: addr})
	}
	slices.SortFunc(accounts, func(a, b account) int { return a.addr.Cmp(b.addr) })
	return accounts
}

func initTestState(t *testing.T) *ethapi.TestBackend {
	// Initialize test accounts
	var (
		accounts = newAccounts(2)
		signer   = types.HomesteadSigner{}
		genesis  = &core.Genesis{
			Config: params.TestChainConfig,
			Alloc: types.GenesisAlloc{
				acc1Addr: {Balance: big.NewInt(params.Ether)},
				acc2Addr: {Balance: big.NewInt(params.Ether)},
			},
		}
		genBlocks = 10
	)

	backend := ethapi.NewTestBackend(t, genBlocks, genesis, ethash.NewFaker(), func(i int, b *core.BlockGen) {
		// init transaction, for this use case no need
		// way to init can refer back to go-ethereum/internal/ethapi/api_test.go
		tx, _ := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: uint64(i), To: &accounts[1].addr, Value: big.NewInt(1000), Gas: params.TxGas, GasPrice: b.BaseFee(), Data: nil}), signer, accounts[0].key)
		b.AddTx(tx)
		b.SetPoS()
		tx.AccessList()
	})

	return backend
}

// Test to cover it is able to read the storage state
func TestNewArbState(t *testing.T) {
	t.Parallel()

	context := context.Background()

	backend := initTestState(t)

	arbState, err := backend.ArbStateByBlockNumber(context, rpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("failed to create arbstate: %v", err)
	}

	if arbState == nil {
		t.Fatalf("failed to create arbstate: %v", err)
	}

	// plan to be empty
	allPricerTxTo, err := arbState.PricerState.TxToAddrs().AllMembers(100)
	if err != nil {
		t.Fatalf("failed to read tx to address from pricer: %v", err)
	}

	require.Equalf(t, len(allPricerTxTo), 0, "test TestNewArbState: size of tx to address not match, want: %d, have: %d", 0, len(allPricerTxTo))

	dummyUsdtAddress := common.HexToAddress("0x8C8B2579353Ae5Bfe84128571914681629A9963d")
	err = arbState.SubAccountState.SetUsdtAddress(dummyUsdtAddress)
	if err != nil {
		t.Fatalf("failed to set usdt address: %v", err)
	}

	usdtAddress, err := arbState.SubAccountState.UsdtAddress()
	if err != nil {
		t.Fatalf("failed to read usdt address: %v", err)
	}

	if usdtAddress.Cmp(dummyUsdtAddress) != 0 {
		t.Fatalf("Usdt address is not tally, want: %s, have: %s", dummyUsdtAddress.String(), usdtAddress.String())
	}

}

// Test to cover it is able to read the storage from vm statedb
func TestNewVmState(t *testing.T) {
	t.Parallel()

	context := context.Background()

	backend := initTestState(t)

	globalGasCap := uint64(1000)
	stateDb, header, err := backend.StateAndHeaderByNumber(context, rpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("get header by number fail: %v", err)
	}

	args := ethapi.TransactionArgs{
		From: &acc1Addr,
		To:   &acc2Addr,
		Data: newRPCBytes(common.Hex2Bytes("f8a8fd6d")), //
	}
	// Get a new instance of the EVM.
	msg, err := args.ToMessage(globalGasCap, header, stateDb, core.MessageGasEstimationMode)
	if err != nil {
		t.Fatalf("arg to message fail: %v", err)
	}

	blockCtx := core.NewEVMBlockContext(header, ethapi.NewChainContext(context, backend), nil)

	// Arbitrum: support NodeInterface.sol by swapping out the message if needed
	var res *core.ExecutionResult
	msg, res, err = core.InterceptRPCMessage(msg, context, stateDb, header, backend, &blockCtx)
	if err != nil || res != nil {
		t.Fatalf("InterceptRPCMessage fail: %v", err)
	}

	vm := backend.GetEVM(context, msg, stateDb, header, &vm.Config{NoBaseFee: true}, nil)

	arbState := NewVmState(&vm.StateDB)

	if arbState == nil {
		t.Fatalf("failed to create arbstate: %v", err)
	}

	// plan to be empty
	allPricerTxTo, err := arbState.PricerState.TxToAddrs().AllMembers(100)
	if err != nil {
		t.Fatalf("failed to read tx to address from pricer: %v", err)
	}

	require.Equalf(t, len(allPricerTxTo), 0, "test TestNewArbState: size of tx to address not match, want: %d, have: %d", 0, len(allPricerTxTo))

	dummyUsdtAddress := common.HexToAddress("0x8C8B2579353Ae5Bfe84128571914681629A9963d")
	err = arbState.SubAccountState.SetUsdtAddress(dummyUsdtAddress)
	if err != nil {
		t.Fatalf("failed to set usdt address: %v", err)
	}

	usdtAddress, err := arbState.SubAccountState.UsdtAddress()
	if err != nil {
		t.Fatalf("failed to read usdt address: %v", err)
	}

	if usdtAddress.Cmp(dummyUsdtAddress) != 0 {
		t.Fatalf("Usdt address is not tally, want: %s, have: %s", dummyUsdtAddress.String(), usdtAddress.String())
	}

}

// Test to cover it is able to read same from statedb and vm statedb
func TestTallyState(t *testing.T) {
	t.Parallel()

	context := context.Background()

	backend := initTestState(t)

	globalGasCap := uint64(1000)
	stateDb, header, err := backend.StateAndHeaderByNumber(context, rpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("get header by number fail: %v", err)
	}

	args := ethapi.TransactionArgs{
		From: &acc1Addr,
		To:   &acc2Addr,
		Data: newRPCBytes(common.Hex2Bytes("f8a8fd6d")), //
	}
	// Get a new instance of the EVM.
	msg, err := args.ToMessage(globalGasCap, header, stateDb, core.MessageGasEstimationMode)
	if err != nil {
		t.Fatalf("arg to message fail: %v", err)
	}

	blockCtx := core.NewEVMBlockContext(header, ethapi.NewChainContext(context, backend), nil)

	// Arbitrum: support NodeInterface.sol by swapping out the message if needed
	var res *core.ExecutionResult
	msg, res, err = core.InterceptRPCMessage(msg, context, stateDb, header, backend, &blockCtx)
	if err != nil || res != nil {
		t.Fatalf("InterceptRPCMessage fail: %v", err)
	}

	vm := backend.GetEVM(context, msg, stateDb, header, &vm.Config{NoBaseFee: true}, nil)

	arbStateVm := NewVmState(&vm.StateDB)
	if arbStateVm == nil {
		t.Fatalf("failed to create arbstate  vm: %v", err)
	}

	arbState, err := backend.ArbStateByBlockNumber(context, rpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("failed to create arbstate: %v", err)
	}
	if arbState == nil {
		t.Fatalf("failed to create arbstate: %v", err)
	}

	// plan to be empty
	allPricerTxTo, err := arbState.PricerState.TxToAddrs().AllMembers(100)
	if err != nil {
		t.Fatalf("failed to read tx to address from pricer: %v", err)
	}

	allVmPricerTxTo, err := arbStateVm.PricerState.TxToAddrs().AllMembers(100)
	if err != nil {
		t.Fatalf("failed to read tx to address from pricer: %v", err)
	}

	require.Equalf(t, len(allPricerTxTo), len(allVmPricerTxTo), "test TestNewArbState: size of tx to address not match, want: %d, have: %d", len(allVmPricerTxTo), len(allPricerTxTo))

	dummyUsdtAddress := common.HexToAddress("0x8C8B2579353Ae5Bfe84128571914681629A9963d")
	err = arbState.SubAccountState.SetUsdtAddress(dummyUsdtAddress)
	if err != nil {
		t.Fatalf("failed to set usdt address: %v", err)
	}

	usdtAddress, err := arbState.SubAccountState.UsdtAddress()
	if err != nil {
		t.Fatalf("failed to read usdt address: %v", err)
	}

	usdtVmAddress, err := arbStateVm.SubAccountState.UsdtAddress()
	if err != nil {
		t.Fatalf("failed to read usdt address: %v", err)
	}

	if usdtAddress.Cmp(*usdtVmAddress) != 0 {
		t.Fatalf("Usdt address is not tally, want: %s, have: %s", dummyUsdtAddress.String(), usdtVmAddress.String())
	}

}
