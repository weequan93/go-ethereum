package pricer

// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

import (
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/burn"
	"github.com/ethereum/go-ethereum/arbitrum-core/arbos/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/offchainlabs/nitro/util/testhelpers"
)

func PricerForTest(t *testing.T) *Pricer {
	storage := storage.NewMemoryBacked(burn.NewSystemBurner(nil, false))
	err := InitializePricer(storage.OpenSubStorage(txFromAddrsSubspace))
	Require(t, err)
	return OpenPricer(storage)
}

func TestPricer(t *testing.T) {
	t.Parallel()

	pricer := PricerForTest(t)

	// validate storing

	members, err := pricer.TxToAddrs().AllMembers(100)
	if err != nil {
		Fail(t, "Fail to read tx to records")
	}
	if len(members) != 0 {
		Fail(t, "Record inside initial pricer tx to should be zero")
	}

	members, err = pricer.TxFromAddrs().AllMembers(100)
	if err != nil {
		require.Fail(t, "Fail to read tx from records %v", err)
	}
	if len(members) != 0 {
		Fail(t, "Record inside initial pricer tx from should be zero")
	}

	validPricerFromAddress := common.HexToAddress("0x9C26a80e21a762eb2809aFd7C123728bF9930Cf1")
	invalidPricerFromAddress := common.HexToAddress("0x94A6713cbF5F589aB51570D0b4cd219792421af2")

	err = pricer.TxFromAddrs().Add(validPricerFromAddress)
	if err != nil {
		require.Fail(t, "Fail to add tx from records %v", err)
	}
	isMember, err := pricer.TxFromAddrs().IsMember(validPricerFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx from member %v", err)
	}
	if isMember != true {
		require.Fail(t, "Check tx from member result incorrect, expected %s, receive %s", true, isMember)
	}

	isMember, err = pricer.TxFromAddrs().IsMember(invalidPricerFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx from member %v", err)
	}
	if isMember == true {
		require.Fail(t, "Check tx from member result incorrect, expected %s, receive %s", false, isMember)
	}

	validPricerToAddress := common.HexToAddress("0x6b20483C964B39da3607cE96BCf4b53794944490")
	invalidPricerToAddress := common.HexToAddress("0x1a76822BF95714D9c32a07477906fF0ddEaBc2f2")

	err = pricer.TxToAddrs().Add(validPricerToAddress)
	if err != nil {
		require.Fail(t, "Fail to add tx to records %v", err)
	}
	isMember, err = pricer.TxToAddrs().IsMember(validPricerToAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx to member %v", err)
	}
	if isMember != true {
		require.Fail(t, "Check tx to member result incorrect, expected %s, receive %s", true, isMember)
	}

	isMember, err = pricer.TxToAddrs().IsMember(invalidPricerToAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx to member %v", err)
	}
	if isMember == true {
		require.Fail(t, "Check tx to member result incorrect, expected %s, receive %s", false, isMember)
	}

	// validate transaction
	isValid := pricer.IsCustomPriceTxCheckAddr(&invalidPricerToAddress)
	if isValid == true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", false, isValid)
	}

	isValid = pricer.IsCustomPriceTxCheckAddr(&validPricerToAddress)
	if isValid != true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", true, isValid)
	}

	var inner types.TxData
	inner = &types.BlobTx{
		To: invalidPricerToAddress,
	}

	tx := types.NewTx(inner)
	isValid = pricer.IsCustomPriceTxCheck(tx)
	if isValid == true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", false, isValid)
	}

	inner = &types.BlobTx{
		To: validPricerToAddress,
	}
	tx = types.NewTx(inner)
	isValid = pricer.IsCustomPriceTxCheck(tx)
	if isValid != true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", true, isValid)
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
