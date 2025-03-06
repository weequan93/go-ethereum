package pricer

import (
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/addressSet"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type Pricer struct {
	storage     *storage.Storage
	txFromAddrs *addressSet.AddressSet
	txToAddrs   *addressSet.AddressSet
}

type SubspaceID []byte

var (
	txFromAddrsSubspace SubspaceID = []byte{0}
	txToAddrsSubspace   SubspaceID = []byte{1}
)

func InitializePricer(sto *storage.Storage) error {
	_ = addressSet.Initialize(sto.OpenSubStorage(txFromAddrsSubspace))
	return addressSet.Initialize(sto.OpenSubStorage(txToAddrsSubspace))
}

func OpenPricer(sto *storage.Storage) *Pricer {
	return &Pricer{
		sto,
		addressSet.OpenAddressSet(sto.OpenSubStorage(txFromAddrsSubspace)),
		addressSet.OpenAddressSet(sto.OpenSubStorage(txToAddrsSubspace)),
	}
}

func (pricer *Pricer) TxFromAddrs() *addressSet.AddressSet {
	return pricer.txFromAddrs
}

func (pricer *Pricer) TxToAddrs() *addressSet.AddressSet {
	return pricer.txToAddrs
}

func (pricer *Pricer) IsCustomPriceTxCheck(tx *types.Transaction) bool {
	if tx != nil {
	}
	if tx != nil && tx.To() != nil {
		IsGaslessContract, err := pricer.TxToAddrs().IsMember(*tx.To())
		if err != nil {
			return false
		}
		return IsGaslessContract
	}
	return false
}

func (pricer *Pricer) IsCustomPriceTxCheckAddr(addr *common.Address) bool {
	if addr == nil {
		return false
	}
	IsGaslessContract, err := pricer.TxToAddrs().IsMember(*addr)
	if err != nil {
		return false
	}
	return IsGaslessContract

}
