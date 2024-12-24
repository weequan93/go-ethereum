package subAccount

// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/burn"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/offchainlabs/nitro/util/testhelpers"
)

var usdtAddress = common.HexToAddress("0x9C26a80e21a762eb2809aFd7C123728bF9930Cf1")
var subAccountOwnerAddress = common.HexToAddress("0x94A6713cbF5F589aB51570D0b4cd219792421af2")
var emptyAddress = common.HexToAddress("")

func SubAccountForTest(t *testing.T) *SubAccountState {
	storage := storage.NewMemoryBacked(burn.NewSystemBurner(nil, false))
	err := InitializeSubAccountState(storage)
	Require(t, err)
	return OpenSubAccountState(storage)
}

func InitSubAccountData(t *testing.T, subAccountState *SubAccountState) {
	err := subAccountState.SetUsdtAddress(usdtAddress)
	Require(t, err)

	err = subAccountState.SubAccountOwner().Add(subAccountOwnerAddress)
	Require(t, err)
}

type UsdtTestCase struct {
	ContractAddress common.Address
	TxData          []byte
	ExpectedResult  bool
}

// test permission and value set
func TestUsdtOperation(t *testing.T) {
	t.Parallel()

	subAccountState := SubAccountForTest(t)
	InitSubAccountData(t, subAccountState)

	// check initial state
	usdtAddr, err := subAccountState.UsdtAddress()
	if err != nil {
		Fail(t, "Fail to read usdt address")
	}

	if usdtAddr.Cmp(usdtAddress) != 0 {
		Fail(t, "Initial state of usdt address is not same, expected = %s, actual = %s", usdtAddress.Hex(), usdtAddr.Hex())
	}

	// reset address
	err = subAccountState.SetUsdtAddress(emptyAddress)
	if err != nil {
		Fail(t, "Fail to set empty usdt address")
	}

	// validate reset correctly
	usdtAddr, err = subAccountState.UsdtAddress()
	if err != nil {
		Fail(t, "Fail to read usdt address")
	}
	if usdtAddr.Cmp(emptyAddress) != 0 {
		Fail(t, "Usdt address after reset is not zero, expected = %s, actual = %s", emptyAddress, usdtAddr.Hex())
	}

	// set again address
	err = subAccountState.SetUsdtAddress(usdtAddress)
	if err != nil {
		Fail(t, "Fail to set usdt address")
	}

	// validate reset correctly
	usdtAddr, err = subAccountState.UsdtAddress()
	if err != nil {
		Fail(t, "Fail to read usdt address")
	}
	if usdtAddr.Cmp(usdtAddress) != 0 {
		Fail(t, "Set of usdt address fail, expected = %s, actual = %s", usdtAddress, usdtAddr.Hex())
	}
}

func TestIsAllowedUsdtAddress(t *testing.T) {
	t.Parallel()

	subAccountState := SubAccountForTest(t)
	InitSubAccountData(t, subAccountState)

	anyFailOpSig1, _ := hex.DecodeString("095ea7b1")
	anyFailOpSig2, _ := hex.DecodeString("095ea1")
	anyFailOpSig3, _ := hex.DecodeString("095ea7b21122")
	fakeUsdtAddress := common.HexToAddress("0x6b20483C964B39da3607cE96BCf4b53794944490")

	testCases := []UsdtTestCase{
		{ContractAddress: usdtAddress, TxData: []byte{}, ExpectedResult: false},
		{ContractAddress: usdtAddress, TxData: anyFailOpSig1, ExpectedResult: false},
		{ContractAddress: usdtAddress, TxData: anyFailOpSig2, ExpectedResult: false},
		{ContractAddress: usdtAddress, TxData: anyFailOpSig3, ExpectedResult: false},

		{ContractAddress: fakeUsdtAddress, TxData: []byte{}, ExpectedResult: false},
		{ContractAddress: fakeUsdtAddress, TxData: anyFailOpSig1, ExpectedResult: false},
		{ContractAddress: fakeUsdtAddress, TxData: anyFailOpSig2, ExpectedResult: false},
		{ContractAddress: fakeUsdtAddress, TxData: anyFailOpSig3, ExpectedResult: false},
		{ContractAddress: fakeUsdtAddress, TxData: ERC20_APPROVE_OP_SIG, ExpectedResult: false},

		{ContractAddress: usdtAddress, TxData: ERC20_APPROVE_OP_SIG, ExpectedResult: true},
	}

	for i, testCaseItem := range testCases {
		result, err := subAccountState.IsAllowedUsdtAddress(testCaseItem.ContractAddress, testCaseItem.TxData)
		if err != nil {
			Fail(t, "Fail to read is allowed sub-account address")
		}
		if result != testCaseItem.ExpectedResult {
			Fail(t, "Fail to match result IsAllowedUsdtAddress for test case %d, expected = %s, actual = %s", i, testCaseItem.ExpectedResult, result)
		}
	}
}

// test sub-account owner and value set
func TestSubAccountOwnerOperation(t *testing.T) {
	t.Parallel()

	subAccountState := SubAccountForTest(t)
	InitSubAccountData(t, subAccountState)

	// check initial state
	ownersAddr, err := subAccountState.SubAccountOwner().AllMembers(100)
	if err != nil {
		Fail(t, "SubAccount owner address length is not same, expected = %d, actual = %d", 2, len(ownersAddr))
	}
	if len(ownersAddr) != 1 {
		Fail(t, "SubAccount owner address length is not same")
	}

	// verify original value is there
	isOwner, err := subAccountState.SubAccountOwner().IsMember(subAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner != true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", true, isOwner)
	}

	// validate can add more than 1 owner
	secondSubAccountOwnerAddress := common.HexToAddress("0x6b20483C964B39da3607cE96BCf4b53794944490")
	isOwner, err = subAccountState.SubAccountOwner().IsMember(secondSubAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner == true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", false, isOwner)
	}

	err = subAccountState.SubAccountOwner().Add(secondSubAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to add new sub-account owner address")
	}

	ownersAddr, err = subAccountState.SubAccountOwner().AllMembers(100)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if len(ownersAddr) != 2 {
		Fail(t, "SubAccount owner address length is not same, expected = %d, actual = %d", 2, len(ownersAddr))
	}

	isOwner, err = subAccountState.SubAccountOwner().IsMember(subAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner != true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", true, isOwner)
	}

	isOwner, err = subAccountState.SubAccountOwner().IsMember(secondSubAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner != true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", true, isOwner)
	}

	// remove secondSubAccountOwnerAddress
	err = subAccountState.SubAccountOwner().Remove(secondSubAccountOwnerAddress, 21)
	if err != nil {
		Fail(t, "Fail to remove sub-account owner address")
	}

	ownersAddr, err = subAccountState.SubAccountOwner().AllMembers(100)
	if err != nil {
		Fail(t, "SubAccount owner address length is not same, expected = %d, actual = %d", 2, len(ownersAddr))
	}
	if len(ownersAddr) != 1 {
		Fail(t, "SubAccount owner address length is not same")
	}

	isOwner, err = subAccountState.SubAccountOwner().IsMember(subAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner != true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", true, isOwner)
	}

	isOwner, err = subAccountState.SubAccountOwner().IsMember(secondSubAccountOwnerAddress)
	if err != nil {
		Fail(t, "Fail to get sub-account owner address")
	}
	if isOwner == true {
		Fail(t, "Checking of sub-account owner fail, expected = %s, actual = %s", true, isOwner)
	}
}

func TestBindRelation(t *testing.T) {
	t.Parallel()

	subAccountState := SubAccountForTest(t)
	InitSubAccountData(t, subAccountState)

	parentAddress := common.HexToAddress("0x9C26a80e21a762eb2809aFd7C123728bF9930Cf1")
	childAddress := common.HexToAddress("0x94A6713cbF5F589aB51570D0b4cd219792421af2")

	child, err := subAccountState.ReadRelationFromParent(parentAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if child.Cmp(common.Address{}) != 0 {
		Fail(t, "Check relationship from parent fail, expected = %s, actual = %s", common.Address{}, child)
	}

	parent, err := subAccountState.ReadRelationFromChild(childAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if parent.Cmp(common.Address{}) != 0 {
		Fail(t, "Check relationship from child fail, expected = %s, actual = %s", common.Address{}, parent)
	}

	// check with existing relation
	err = subAccountState.BindRelation(parentAddress, childAddress, big.NewInt(0))
	if err != nil {
		Fail(t, "Fail to bind relation")
	}

	child, err = subAccountState.ReadRelationFromParent(parentAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if child.Cmp(childAddress) != 0 {
		Fail(t, "Check relationship from parent fail, expected = %s, actual = %s", childAddress, child)
	}

	parent, err = subAccountState.ReadRelationFromChild(childAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if parent.Cmp(parentAddress) != 0 {
		Fail(t, "Check relationship from child fail, expected = %s, actual = %s", parentAddress, parent)
	}

	parentAddr, err := subAccountState.GetParentAddress(child, &usdtAddress, ERC20_APPROVE_OP_SIG)
	if parentAddr.Cmp(parentAddress) != 0 {
		Fail(t, "Check relationship from child fail, expected = %s, actual = %s", parentAddress, parentAddr)
	}

	// revoke it
	err = subAccountState.RevokeRelation(parentAddress)
	if err != nil {
		Fail(t, "Fail to revoke relation")
	}
	child, err = subAccountState.ReadRelationFromParent(parentAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if child.Cmp(common.Address{}) != 0 {
		Fail(t, "Check relationship from parent fail, expected = %s, actual = %s", common.Address{}, child)
	}

	parent, err = subAccountState.ReadRelationFromChild(childAddress)
	if err != nil {
		Fail(t, "Fail to read relation")
	}

	if parent.Cmp(common.Address{}) != 0 {
		Fail(t, "Check relationship from child fail, expected = %s, actual = %s", common.Address{}, parent)
	}

}

func TestSession(t *testing.T) {
	// BindRelation
	// IsValidSession
}

func TestHasUsedHash(t *testing.T) {
	t.Parallel()

	subAccountState := SubAccountForTest(t)
	InitSubAccountData(t, subAccountState)

	key := common.HexToHash("0x12345")

	isSpend, err := subAccountState.HasUsedHash(key)
	if err != nil {
		Fail(t, "Fail check has used hash", err)
	}

	if isSpend == true {
		Fail(t, "Check for has usd hash fail, expected = %v, actual = %v. ", false, isSpend)
	}

	err = subAccountState.SetUsedHash(key)
	if err != nil {
		Fail(t, "Fail set has used hash", err)
	}

	isSpend, err = subAccountState.HasUsedHash(key)
	if err != nil {
		Fail(t, "Fail check has used hash", err)
	}

	if isSpend == false {
		Fail(t, "Check for has usd hash fail, expected = %v, actual = %v. ", true, isSpend)
	}
}

func Require(t *testing.T, err error, printables ...interface{}) {
	t.Helper()
	testhelpers.RequireImpl(t, err, printables...)
}

func Fail(t *testing.T, printables ...interface{}) {
	t.Helper()
	testhelpers.FailImpl(t, printables...)
}
