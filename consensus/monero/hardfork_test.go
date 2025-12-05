package monero

import (
	"testing"
)

func TestNetworkHardForkSupportedMainnet(t *testing.T) {
	hardForks := NetworkHardFork(MainNetwork)
	f := hardForks[len(hardForks)-1]

	if f.Version < HardForkSupportedVersion {
		t.Fatalf("supported version %d greater than latest mainnet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
	if f.Version > HardForkSupportedVersion {
		t.Fatalf("supported version %d smaller than latest mainnet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
}

func TestNetworkHardForkSupportedTestnet(t *testing.T) {
	hardForks := NetworkHardFork(TestNetwork)
	f := hardForks[len(hardForks)-1]

	if f.Version < HardForkSupportedVersion {
		t.Fatalf("supported version %d greater than latest testnet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
	if f.Version > HardForkSupportedVersion {
		t.Fatalf("supported version %d smaller than latest testnet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
}

func TestNetworkHardForkSupportedStagenet(t *testing.T) {
	hardForks := NetworkHardFork(StageNetwork)
	f := hardForks[len(hardForks)-1]

	if f.Version < HardForkSupportedVersion {
		t.Fatalf("supported version %d greater than latest stagenet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
	if f.Version > HardForkSupportedVersion {
		t.Fatalf("supported version %d smaller than latest stagenet hardfork, last %d", HardForkSupportedVersion, f.Version)
	}
}
