// +build !debug
// +build !2k
// +build !testground
// +build !calibnet
// +build !nerpanet
// +build !butterflynet

package build

import (
	"math"
	"os"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/lotus/chain/actors/policy"
	miner0 "github.com/filecoin-project/specs-actors/actors/builtin/miner"
	builtin2 "github.com/filecoin-project/specs-actors/v2/actors/builtin"
)

var DrandSchedule = map[abi.ChainEpoch]DrandEnum{
	0: DrandIncentinet,
}

const BootstrappersFile = "mainnet.pi"
const GenesisFile = "mainnet.car"

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

const UpgradeTapeHeight = 10_000_002

// This signals our tentative epoch for mainnet launch. Can make it later, but not earlier.
// Miners, clients, developers, custodians all need time to prepare.
// We still have upgrades and state changes to do, but can happen after signaling timing here.
const UpgradeLiftoffHeight = 10_000_003

const UpgradeKumquatHeight = 10_000_004

const UpgradeCalicoHeight = 10_000_005
const UpgradePersianHeight = UpgradeCalicoHeight + (builtin2.EpochsInHour * 60)

const UpgradeOrangeHeight = 336458

// 2020-12-22T02:00:00Z
const UpgradeClausHeight = 343200

// 2021-03-04T00:00:30Z
var UpgradeActorsV3Height = abi.ChainEpoch(550321)

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

	if os.Getenv("LOTUS_DISABLE_V3_ACTOR_MIGRATION") == "1" {
		UpgradeActorsV3Height = math.MaxInt64
	}

	Devnet = false

	BuildType = BuildMainnet
}

const BlockDelaySecs = uint64(builtin2.EpochDurationSeconds)

const PropagationDelaySecs = uint64(6)

// BootstrapPeerThreshold is the minimum number peers we need to track for a sync worker to start
const BootstrapPeerThreshold = 4
