package arbitrum_core

import (
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/burn"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/pricer"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/subAccount"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
)

type ArbState struct {
	PricerState     *pricer.Pricer
	SubAccountState *subAccount.SubAccountState
}

type SubspaceID []byte

var (
	pricerSubspace     SubspaceID = []byte{8}
	subAccountSubspace SubspaceID = []byte{10}
)

func New(state *state.StateDB) *ArbState {
	burner := burn.NewSystemBurner(nil, true)
	backingStorage := storage.NewGeth(state, burner)
	subAccountState := subAccount.OpenSubAccountState(backingStorage.OpenSubStorage(subAccountSubspace))
	pricerSubState := pricer.OpenPricer(backingStorage.OpenSubStorage(pricerSubspace))

	return &ArbState{
		SubAccountState: subAccountState,
		PricerState:     pricerSubState,
	}
}

func NewVmState(state *vm.StateDB) *ArbState {
	burner := burn.NewSystemBurner(nil, true)
	backingStorage := storage.NewGeth(*state, burner)
	subAccountState := subAccount.OpenSubAccountState(backingStorage.OpenSubStorage(subAccountSubspace))
	pricerSubState := pricer.OpenPricer(backingStorage.OpenSubStorage(pricerSubspace))

	return &ArbState{
		SubAccountState: subAccountState,
		PricerState:     pricerSubState,
	}
}
