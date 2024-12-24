package core

import (
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/offchainlabs/nitro/arbos/burn"
	"github.com/offchainlabs/nitro/arbos/pricer"
	"github.com/offchainlabs/nitro/arbos/storage"
	"github.com/offchainlabs/nitro/arbos/subAccount"
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
	subAccountState := subAccount.OpenSubAccountState(backingStorage.OpenCachedSubStorage(subAccountSubspace))
	pricerSubState := pricer.OpenPricer(backingStorage.OpenSubStorage(pricerSubspace))

	return &ArbState{
		SubAccountState: subAccountState,
		PricerState:     pricerSubState,
	}
}

func NewVmState(state *vm.StateDB) *ArbState {
	burner := burn.NewSystemBurner(nil, true)
	backingStorage := storage.NewGeth(*state, burner)
	subAccountState := subAccount.OpenSubAccountState(backingStorage.OpenCachedSubStorage(subAccountSubspace))
	pricerSubState := pricer.OpenPricer(backingStorage.OpenSubStorage(pricerSubspace))

	return &ArbState{
		SubAccountState: subAccountState,
		PricerState:     pricerSubState,
	}
}
