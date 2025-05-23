// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package subAccount

import (
	"bytes"
	"encoding/hex"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/addressMap"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/addressSet"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/common"
	"math/big"
)

type Offset uint64

var (
	// web3.eth.abi.encodeFunctionSignature(approve((address,address))'
	ERC20_APPROVE_OP_SIG, _ = hex.DecodeString("095ea7b3")
)

type SubAccountState struct {
	storage *storage.Storage
	//parentChildRelation *storage.Storage
	//childParentRelation *storage.Storage
	parentChildRelation *addressMap.AddressMap
	childParentRelation *addressMap.AddressMap
	relationTimer       *storage.Storage
	allowedAddress      *addressSet.AddressSet
	subAccountOwner     *addressSet.AddressSet
	hashSpend           *storage.Storage
	usdtAddress         *addressSet.AddressSet
}

type SubspaceID []byte

var (
	parentChildRelationOffset SubspaceID = []byte{0}
	childParentRelationOffset SubspaceID = []byte{1}
	allowedAddressOffSet      SubspaceID = []byte{2}
	subAccountOwnerOffSet     SubspaceID = []byte{3}
	relationTimerOffSet       SubspaceID = []byte{4}
	hashSpendOffset           SubspaceID = []byte{5}
	usdtAddressOffset         SubspaceID = []byte{6}
)

func InitializeSubAccountState(sto *storage.Storage) error {
	//sto.OpenSubStorage(parentChildRelationOffset)
	//sto.OpenSubStorage(childParentRelationOffset)
	addressMap.Initialize(sto.OpenSubStorage(parentChildRelationOffset))
	addressMap.Initialize(sto.OpenSubStorage(childParentRelationOffset))
	sto.OpenSubStorage(relationTimerOffSet)
	sto.OpenSubStorage(hashSpendOffset)
	_ = addressSet.Initialize(sto.OpenSubStorage(allowedAddressOffSet))
	_ = addressSet.Initialize(sto.OpenSubStorage(usdtAddressOffset))
	return addressSet.Initialize(sto.OpenSubStorage(subAccountOwnerOffSet))
}

func OpenSubAccountState(sto *storage.Storage) *SubAccountState {

	return &SubAccountState{
		storage: sto,
		//parentChildRelation: sto.OpenSubStorage(parentChildRelationOffset),
		//childParentRelation: sto.OpenSubStorage(childParentRelationOffset),
		parentChildRelation: addressMap.OpenAddressMap(sto.OpenSubStorage(parentChildRelationOffset)),
		childParentRelation: addressMap.OpenAddressMap(sto.OpenSubStorage(childParentRelationOffset)),
		allowedAddress:      addressSet.OpenAddressSet(sto.OpenSubStorage(allowedAddressOffSet)),
		subAccountOwner:     addressSet.OpenAddressSet(sto.OpenSubStorage(subAccountOwnerOffSet)),
		relationTimer:       sto.OpenSubStorage(relationTimerOffSet),
		hashSpend:           sto.OpenSubStorage(hashSpendOffset),
		usdtAddress:         addressSet.OpenAddressSet(sto.OpenSubStorage(usdtAddressOffset)),
	}
}

func (subAccountState *SubAccountState) BindRelation(parentAccount common.Address, subAccount common.Address, timestamp *big.Int) (err error) {
	// revalidate old-sub-account
	oldSubAccount, err := subAccountState.ReadRelationFromParent(parentAccount)
	if err != nil {
		return err
	}
	err = subAccountState.childParentRelation.Remove(oldSubAccount, 16)
	if err != nil {
		return err
	}

	err = subAccountState.parentChildRelation.Remove(parentAccount, 16)
	if err != nil {
		return err
	}

	//err = subAccountState.parentChildRelation.Set(common.BytesToHash(parentAccount.Bytes()), common.BytesToHash(subAccount.Bytes()))
	err = subAccountState.parentChildRelation.Add(parentAccount, subAccount)
	if err != nil {
		return err
	}

	//err = subAccountState.childParentRelation.Set(common.BytesToHash(subAccount.Bytes()), common.BytesToHash(parentAccount.Bytes()))
	err = subAccountState.childParentRelation.Add(subAccount, parentAccount)
	if err != nil {
		return err
	}

	//err = subAccountState.relationTimer.Set(common.BytesToHash(subAccount.Bytes()), common.BytesToHash(timestamp.Bytes()))
	//if err != nil {
	//	return err
	//}

	return nil
}

func (subAccountState *SubAccountState) RevokeRelation(parentAccount common.Address) (err error) {

	// get child account
	subAccount, err := subAccountState.ReadRelationFromParent(parentAccount)
	if err != nil {
		return err
	}
	//err = subAccountState.childParentRelation.Clear(common.BytesToHash(subAccount.Bytes()))
	err = subAccountState.childParentRelation.Remove(subAccount, 16)
	if err != nil {
		return err
	}

	//err = subAccountState.parentChildRelation.Clear(common.BytesToHash(parentAccount.Bytes()))
	err = subAccountState.parentChildRelation.Remove(parentAccount, 16)
	if err != nil {
		return err
	}

	//subAccountState.relationTimer.Set(common.BytesToHash(subAccount.Bytes()), common.BytesToHash(util.Int64ToBytes(0)))

	return nil
}

func (subAccountState *SubAccountState) ReadRelationFromParent(parentAccount common.Address) (common.Address, error) {
	//childAccount, err := subAccountState.parentChildRelation.Get(common.BytesToHash(parentAccount.Bytes()))
	childAccount, err := subAccountState.parentChildRelation.GetMember(parentAccount)
	return childAccount, err
}

func (subAccountState *SubAccountState) ReadRelationFromChild(subAccount common.Address) (common.Address, error) {
	//parentAccount, err := subAccountState.childParentRelation.Get(common.BytesToHash(subAccount.Bytes()))
	parentAccount, err := subAccountState.childParentRelation.GetMember(subAccount)
	return parentAccount, err
}

func (subAccountState *SubAccountState) AllowedAddress() *addressSet.AddressSet {
	return subAccountState.allowedAddress
}

func (subAccountState *SubAccountState) SubAccountOwner() *addressSet.AddressSet {
	return subAccountState.subAccountOwner
}

func (subAccountState *SubAccountState) SetUsedHash(hash common.Hash) error {
	err := subAccountState.hashSpend.Set(hash, common.HexToHash("0x1"))

	return err
}

func (subAccountState *SubAccountState) ResetAllRelationship() error {

	err := subAccountState.childParentRelation.Clear()
	if err != nil {
		return err
	}

	err = subAccountState.parentChildRelation.Clear()
	if err != nil {
		return err
	}

	return err
}

func (subAccountState *SubAccountState) ResetAllRelationshipByIndex(size uint64) error {

	err := subAccountState.childParentRelation.ClearBySize(size)
	if err != nil {
		return err
	}

	err = subAccountState.parentChildRelation.ClearBySize(size)
	if err != nil {
		return err
	}

	return err
}

func (subAccountState *SubAccountState) ResetAllRelationshipByPosition(addr common.Address) error {

	err := subAccountState.childParentRelation.Remove(addr, 16)
	if err != nil {
		return err
	}

	err = subAccountState.parentChildRelation.Remove(addr, 16)
	if err != nil {
		return err
	}

	return err
}

func (subAccountState *SubAccountState) HasUsedHash(hash common.Hash) (bool, error) {
	hashContent, err := subAccountState.hashSpend.Get(hash)
	hasUsed := false
	if hashContent.Cmp(common.HexToHash("0x1")) == 0 {
		hasUsed = true
	}
	return hasUsed, err
}

func (subAccountState *SubAccountState) GetParentAddress(subAccount common.Address, contractAddress *common.Address, txData []byte) (*common.Address, error) {
	if contractAddress == nil {
		return nil, nil
	}
	parentAccount, _ := subAccountState.ReadRelationFromChild(subAccount)
	if parentAccount.Cmp(common.Address{}) != 0 {
		isAllowedAddress, err := subAccountState.AllowedAddress().IsMember(*contractAddress)
		if err != nil {
			return nil, err
		}

		isAllowedUsdtAddress, err := subAccountState.IsAllowedUsdtAddress(*contractAddress, txData)

		if isAllowedAddress || isAllowedUsdtAddress {

			//isValidSession, _, _, err := subAccountState.IsValidSession(subAccount)
			//if err != nil {
			//	return nil, err
			//}
			//
			//if isValidSession == false {
			//	// over current time
			//	return nil, err
			//}
			return &parentAccount, nil
		}
	}

	return nil, nil
}

//func (subAccountState *SubAccountState) IsValidSession(subAccount common.Address) (bool, *big.Int, *big.Int, error) {
//	relationTimeHash, err := subAccountState.relationTimer.Get(common.BytesToHash(subAccount.Bytes()))
//	if err != nil {
//		return false, nil, nil, err
//	}
//
//	relationTime := new(big.Int).SetBytes(relationTimeHash.Bytes())
//	relationEndTime := big.NewInt(0)
//	relationEndTime.Add(relationTime, big.NewInt(60*60*24*7))
//
//	currentTimestamp := big.NewInt(time.Now().Unix())
//
//	if currentTimestamp.Cmp(relationEndTime) > 0 {
//		// over current time
//		return false, relationTime, relationEndTime, nil
//	}
//
//	return true, relationTime, relationEndTime, nil
//}

func (subAccountState *SubAccountState) IsAllowedUsdtAddress(contractAddress common.Address, txData []byte) (bool, error) {
	isUsdtMember, err := subAccountState.usdtAddress.IsMember(contractAddress)

	if err != nil {
		return true, err
	}

	if isUsdtMember == true {
		if len(txData) <= 4 {
			return false, nil
		}
		// is usdt address, cmp op
		if bytes.Equal(txData[:4], ERC20_APPROVE_OP_SIG) {
			return true, nil
		} else {
			return false, nil
		}
	}
	return false, nil
}

func (subAccountState *SubAccountState) UsdtAddress() *addressSet.AddressSet {
	return subAccountState.usdtAddress
}

func (subAccountState *SubAccountState) ReadAccountGranted(parentAccount common.Address) (common.Address, error) {
	//parentAccount, err := subAccountState.childParentRelation.Get(common.BytesToHash(subAccount.Bytes()))
	subAccount, err := subAccountState.parentChildRelation.GetMember(parentAccount)
	return subAccount, err
}
