package blacklist

import (
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/addressSet"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type Blacklist struct {
	storage        *storage.Storage
	blackListOwner *addressSet.AddressSet
	txFromAddrs    *addressSet.AddressSet
	txToAddrs      *addressSet.AddressSet
}

type SubspaceID []byte

var (
	blackListOwnerOffSet SubspaceID = []byte{0}
	txFromAddrsSubspace  SubspaceID = []byte{1}
	txToAddrsSubspace    SubspaceID = []byte{2}
)

func InitializeBlacklist(sto *storage.Storage) error {
	_ = addressSet.Initialize(sto.OpenSubStorage(blackListOwnerOffSet))
	_ = addressSet.Initialize(sto.OpenSubStorage(txFromAddrsSubspace))
	return addressSet.Initialize(sto.OpenSubStorage(txToAddrsSubspace))
}

func OpenBlacklist(sto *storage.Storage) *Blacklist {
	return &Blacklist{
		sto,
		addressSet.OpenAddressSet(sto.OpenSubStorage(blackListOwnerOffSet)),
		addressSet.OpenAddressSet(sto.OpenSubStorage(txFromAddrsSubspace)),
		addressSet.OpenAddressSet(sto.OpenSubStorage(txToAddrsSubspace)),
	}
}

func (blacklist *Blacklist) BlacklistOwner() *addressSet.AddressSet {
	return blacklist.blackListOwner
}

func (blacklist *Blacklist) TxFromAddrs() *addressSet.AddressSet {
	return blacklist.txFromAddrs
}

func (blacklist *Blacklist) TxToAddrs() *addressSet.AddressSet {
	return blacklist.txToAddrs
}

func (blacklist *Blacklist) IsBlacklistTxCheck(from *common.Address, tx *types.Transaction) bool {

	if tx != nil && tx.To() != nil {
		addr := common.HexToAddress(tx.To().String())
		isBlacklistToContract, err := blacklist.TxToAddrs().IsMember(addr)
		if err != nil {
			return false
		}

		if isBlacklistToContract == true {
			return true
		}
	}

	if from != nil {
		//addr := common.HexToAddress(from.String())
		isBlacklistFromContract, err := blacklist.TxFromAddrs().IsMember(*from)
		if err != nil {
			return false
		}

		if isBlacklistFromContract == true {
			return true
		}

	}
	return false
}

func (blacklist *Blacklist) IsBlacklistAddrCheck(addr *common.Address) bool {
	if addr == nil {
		return false
	}
	isBlacklistToAddress, err := blacklist.TxToAddrs().IsMember(*addr)
	if err != nil {
		return false
	}

	if isBlacklistToAddress == true {
		return true
	}

	isBlacklistFromAddress, err := blacklist.TxFromAddrs().IsMember(*addr)
	if err != nil {
		return false
	}

	if isBlacklistFromAddress == true {
		return true
	}

	return false
}
