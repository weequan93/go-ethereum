package blacklist

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

func BlacklistForTest(t *testing.T) *Blacklist {
	storage := storage.NewMemoryBacked(burn.NewSystemBurner(nil, false))
	err := InitializeBlacklist(storage.OpenSubStorage(txFromAddrsSubspace))
	Require(t, err)
	return OpenBlacklist(storage)
}

func TestBlacklist(t *testing.T) {
	t.Parallel()

	blacklist := BlacklistForTest(t)

	// validate storing

	members, err := blacklist.TxToAddrs().AllMembers(100)
	if err != nil {
		Fail(t, "Fail to read tx to records")
	}
	if len(members) != 0 {
		Fail(t, "Record inside initial blacklist tx to should be zero")
	}

	members, err = blacklist.TxFromAddrs().AllMembers(100)
	if err != nil {
		require.Fail(t, "Fail to read tx from records %v", err)
	}
	if len(members) != 0 {
		Fail(t, "Record inside initial blacklist tx from should be zero")
	}

	validBlacklistFromAddress := common.HexToAddress("0x9C26a80e21a762eb2809aFd7C123728bF9930Cf1")
	invalidBlacklistFromAddress := common.HexToAddress("0x94A6713cbF5F589aB51570D0b4cd219792421af2")

	err = blacklist.TxFromAddrs().Add(validBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to add tx from records %v", err)
	}
	isMember, err := blacklist.TxFromAddrs().IsMember(validBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx from member %v", err)
	}
	if isMember != true {
		require.Fail(t, "Check tx from member result incorrect, expected %s, receive %s", true, isMember)
	}

	isMember, err = blacklist.TxFromAddrs().IsMember(invalidBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx from member %v", err)
	}
	if isMember == true {
		require.Fail(t, "Check tx from member result incorrect, expected %s, receive %s", false, isMember)
	}

	validBlacklistToAddress := common.HexToAddress("0x6b20483C964B39da3607cE96BCf4b53794944490")
	invalidBlacklistToAddress := common.HexToAddress("0x1a76822BF95714D9c32a07477906fF0ddEaBc2f2")

	err = blacklist.TxToAddrs().Add(validBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to add tx to records %v", err)
	}
	isMember, err = blacklist.TxToAddrs().IsMember(validBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx to member %v", err)
	}
	if isMember != true {
		require.Fail(t, "Check tx to member result incorrect, expected %s, receive %s", true, isMember)
	}

	isMember, err = blacklist.TxToAddrs().IsMember(invalidBlacklistFromAddress)
	if err != nil {
		require.Fail(t, "Fail to check tx to member %v", err)
	}
	if isMember == true {
		require.Fail(t, "Check tx to member result incorrect, expected %s, receive %s", false, isMember)
	}

	// validate transaction
	isValid := blacklist.IsBlacklistAddrCheck(&invalidBlacklistFromAddress)
	if isValid == true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", false, isValid)
	}

	isValid = blacklist.IsBlacklistAddrCheck(&validBlacklistToAddress)
	if isValid != true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", true, isValid)
	}

	var inner types.TxData
	inner = &types.BlobTx{
		To: invalidBlacklistToAddress,
	}

	tx := types.NewTx(inner)
	isValid = blacklist.IsBlacklistTxCheck(nil, tx)
	if isValid == true {
		require.Fail(t, "Check valid to address fail , expected %s, receive %s", false, isValid)
	}

	inner = &types.BlobTx{
		To: validBlacklistToAddress,
	}
	tx = types.NewTx(inner)
	isValid = blacklist.IsBlacklistTxCheck(nil, tx)
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
