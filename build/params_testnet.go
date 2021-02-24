// +build !debug
// +build !2k
// +build !testground

package build

import (
	"math"
	"os"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/chain/actors/policy"

	builtin0 "github.com/filecoin-project/specs-actors/actors/builtin"
	miner0 "github.com/filecoin-project/specs-actors/actors/builtin/miner"
)

var DrandSchedule = map[abi.ChainEpoch]DrandEnum{
	0: DrandIncentinet,
}

const UpgradeCreeperHeight = 54720
const UpgradeBreezeHeight = 51910
const BreezeGasTampingDuration = 120
const RcPos = -2640

const UpgradeSmokeHeight = 72070

const UpgradeIgnitionHeight = 118150
const UpgradeRefuelHeight = 132550
const UpgradeAmplifierHeight = 172870
const UpgradeHogwartsHeight = 276550
const UpgradeSiriusHeight = 345670

var UpgradeActorsV2Height = abi.ChainEpoch(10_000_001)

// This signals our tentative epoch for mainnet launch. Can make it later, but not earlier.
// Miners, clients, developers, custodians all need time to prepare.
// We still have upgrades and state changes to do, but can happen after signaling timing here.
const UpgradeLiftoffHeight = 10_000_002

func init() {
	miner0.UpgradeRcHeight = UpgradeBreezeHeight + RcPos
	miner0.InitialPleFactorHeight = UpgradeAmplifierHeight
	policy.SetConsensusMinerMinPower(abi.NewStoragePower(20 << 30))
	policy.SetSupportedProofTypes(
		abi.RegisteredSealProof_StackedDrg16GiBV1,
		abi.RegisteredSealProof_StackedDrg4GiBV1,
	)

	if os.Getenv("LOTUS_USE_TEST_ADDRESSES") != "1" {
		SetAddressNetwork(address.Mainnet)
	}

	if os.Getenv("LOTUS_DISABLE_V2_ACTOR_MIGRATION") == "1" {
		UpgradeActorsV2Height = math.MaxInt64
	}

	Devnet = false
}

const BlockDelaySecs = uint64(builtin0.EpochDurationSeconds)

const PropagationDelaySecs = uint64(6)
