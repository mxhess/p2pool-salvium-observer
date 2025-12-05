package sidechain

import (
	"errors"
	"fmt"
	"strconv"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/randomx"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

type NetworkType int

const (
	NetworkInvalid NetworkType = iota
	NetworkMainnet
	NetworkTestnet
	NetworkStagenet
)

const (
	UncleBlockDepth   = 3
	MaxTxOutputReward = (1 << 56) - 1
)

type ConsensusProvider interface {
	Consensus() *Consensus
}

func (n NetworkType) String() string {
	switch n {
	case NetworkInvalid:
		return "invalid"
	case NetworkMainnet:
		return "mainnet"
	case NetworkTestnet:
		return "testnet"
	case NetworkStagenet:
		return "stagenet"
	}
	return ""
}

func (n NetworkType) SubaddressNetwork() (uint8, error) {
	switch n {
	case NetworkInvalid:
		return 0, errors.New("invalid network")
	case NetworkMainnet:
		return monero.SubAddressMainNetwork, nil
	case NetworkTestnet:
		return monero.SubAddressTestNetwork, nil
	case NetworkStagenet:
		return monero.SubAddressStageNetwork, nil
	}
	return 0, errors.New("unknown network")
}

func (n NetworkType) AddressNetwork() (uint8, error) {
	switch n {
	case NetworkInvalid:
		return 0, errors.New("invalid network")
	case NetworkMainnet:
		return monero.MainNetwork, nil
	case NetworkTestnet:
		return monero.TestNetwork, nil
	case NetworkStagenet:
		return monero.StageNetwork, nil
	}
	return 0, errors.New("unknown network")
}

func (n NetworkType) MustAddressNetwork() uint8 {
	network, err := n.AddressNetwork()
	if err != nil {
		panic(err)
	}
	return network
}

func (n NetworkType) MarshalJSON() ([]byte, error) {
	return []byte("\"" + n.String() + "\""), nil
}

func (n *NetworkType) UnmarshalJSON(b []byte) error {
	var s string
	if err := utils.UnmarshalJSON(b, &s); err != nil {
		return err
	}

	switch s {
	case "invalid":
		*n = NetworkInvalid
	case "", "mainnet": //special case for config.json
		*n = NetworkMainnet
	case "testnet":
		*n = NetworkTestnet
	case "stagenet":
		*n = NetworkStagenet

	default:
		return fmt.Errorf("unknown network type %s", s)
	}

	return nil
}

type Consensus struct {
	NetworkType       NetworkType `json:"network_type"`
	PoolName          string      `json:"name"`
	PoolPassword      string      `json:"password"`
	TargetBlockTime   uint64      `json:"block_time"`
	MinimumDifficulty uint64      `json:"min_diff"`
	ChainWindowSize   uint64      `json:"pplns_window"`
	UnclePenalty      uint64      `json:"uncle_penalty"`

	// Extra additional string to add for testing usually
	Extra string `json:"extra,omitempty"`

	// HardFork optional hardfork information for p2pool
	// If empty it will be filled with the default hardfork list to the corresponding NetworkType
	// Note: this is not supported by p2pool itself
	HardForks []monero.HardFork `json:"hard_forks,omitempty"`

	hasher randomx.Hasher

	Id types.Hash `json:"id"`
}

const SmallestMinimumDifficulty = 100000
const LargestMinimumDifficulty = 1000000000

func NewConsensus(networkType NetworkType, poolName, poolPassword, extra string, targetBlockTime, minimumDifficulty, chainWindowSize, unclePenalty uint64) *Consensus {
	c := &Consensus{
		NetworkType:       networkType,
		PoolName:          poolName,
		PoolPassword:      poolPassword,
		TargetBlockTime:   targetBlockTime,
		MinimumDifficulty: minimumDifficulty,
		ChainWindowSize:   chainWindowSize,
		UnclePenalty:      unclePenalty,
		Extra:             extra,
	}

	if !c.verify() {
		return nil
	}
	return c
}

func NewConsensusFromJSON(data []byte) (*Consensus, error) {
	var c Consensus
	if err := utils.UnmarshalJSON(data, &c); err != nil {
		return nil, err
	}

	if !c.verify() {
		return nil, errors.New("could not verify")
	}

	return &c, nil
}

func (c *Consensus) verify() bool {

	if c.PoolName == "default" {
		//p2pool changed consensus config to use default instead of original value
		if ConsensusDefault.NetworkType == c.NetworkType &&
			ConsensusDefault.PoolPassword == c.PoolPassword &&
			ConsensusDefault.TargetBlockTime == c.TargetBlockTime &&
			ConsensusDefault.MinimumDifficulty == c.MinimumDifficulty &&
			ConsensusDefault.ChainWindowSize == c.ChainWindowSize &&
			ConsensusDefault.UnclePenalty == c.UnclePenalty &&
			ConsensusDefault.Extra == c.Extra {
			c.PoolName = ConsensusDefault.PoolName
		}
	}

	if len(c.PoolName) > 128 {
		return false
	}

	if len(c.PoolPassword) > 128 {
		return false
	}

	if c.TargetBlockTime < 1 || c.TargetBlockTime > monero.BlockTime {
		return false
	}

	if c.NetworkType == NetworkMainnet && c.MinimumDifficulty < SmallestMinimumDifficulty || c.MinimumDifficulty > LargestMinimumDifficulty {
		return false
	}

	if c.ChainWindowSize < 60 || c.ChainWindowSize > 2160 {
		return false
	}

	if c.UnclePenalty < 1 || c.UnclePenalty > 99 {
		return false
	}

	var emptyHash types.Hash
	c.Id = c.CalculateId()
	if c.Id == emptyHash {
		return false
	}

	if len(c.HardForks) == 0 {
		switch c.NetworkType {
		case NetworkMainnet:
			c.HardForks = p2poolMainNetHardForks
		case NetworkTestnet:
			c.HardForks = p2poolTestNetHardForks
		case NetworkStagenet:
			c.HardForks = p2poolStageNetHardForks
		default:
			utils.Panicf("invalid network type for determining hardfork")
		}
	}

	return true
}

func (c *Consensus) CalculateSideTemplateId(share *PoolBlock) (result types.Hash) {
	return c.CalculateSideTemplateIdPreAllocated(share, make([]byte, 0, max(share.Main.BufferLength(), share.Side.BufferLength(share.ShareVersion()))))
}

func (c *Consensus) CalculateSideTemplateIdPreAllocated(share *PoolBlock, buf []byte) (result types.Hash) {
	h := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(h)

	buf, _ = share.Main.SideChainHashingBlob(buf, true)
	_, _ = h.Write(buf)
	buf, _ = share.Side.AppendBinary(buf[:0], share.ShareVersion())
	_, _ = h.Write(buf)
	_, _ = h.Write(c.Id[:])
	crypto.HashFastSum(h, result[:])
	return result
}

func (c *Consensus) CalculateSideChainIdFromBlobs(mainBlob, sideBlob []byte) (result types.Hash) {
	h := crypto.GetKeccak256Hasher()
	defer crypto.PutKeccak256Hasher(h)

	_, _ = h.Write(mainBlob)
	_, _ = h.Write(sideBlob)

	_, _ = h.Write(c.Id[:])

	crypto.HashFastSum(h, result[:])
	return result
}

func (c *Consensus) IsDefault() bool {
	return c.Id == ConsensusDefault.Id
}

func (c *Consensus) IsMini() bool {
	return c.Id == ConsensusMini.Id
}

func (c *Consensus) IsNano() bool {
	return c.Id == ConsensusNano.Id
}

func (c *Consensus) DefaultPort() uint16 {
	if c.IsNano() {
		return 37890
	} else if c.IsMini() {
		return 37888
	}
	return 37889
}

func (c *Consensus) SeedNode() string {
	if nodes := c.SeedNodes(); len(nodes) > 0 {
		return nodes[0]
	}
	return ""
}

func (c *Consensus) SeedNodes() []string {
	if c.IsNano() {
		return []string{"seeds-nano.p2pool.io", "nano.p2poolpeers.net", "nano.gupax.io"}
	} else if c.IsMini() {
		return []string{"seeds-mini.p2pool.io", "mini.p2poolpeers.net", "mini.gupax.io"}
	} else if c.IsDefault() {
		return []string{"seeds.p2pool.io", "main.p2poolpeers.net", "main.gupax.io"}
	}
	return nil
}

func (c *Consensus) InitHasher(n int, flags ...randomx.Flag) error {
	if c.hasher != nil {
		c.hasher.Close()
	}
	var err error
	c.hasher, err = randomx.NewRandomX(n, flags...)
	if err != nil {
		return err
	}
	return nil
}

func (c *Consensus) GetHasher() randomx.Hasher {
	if c.hasher == nil {
		panic("hasher has not been initialized in consensus")
	}
	return c.hasher
}

func (c *Consensus) CalculateId() types.Hash {
	var buf []byte
	if c.Extra != "" {
		buf = append(buf, c.Extra...)
		buf = append(buf, 0)
	}
	buf = append(buf, c.NetworkType.String()...)
	buf = append(buf, 0)
	buf = append(buf, c.PoolName...)
	buf = append(buf, 0)
	buf = append(buf, c.PoolPassword...)
	buf = append(buf, 0)
	buf = append(buf, strconv.FormatUint(c.TargetBlockTime, 10)...)
	buf = append(buf, 0)
	buf = append(buf, strconv.FormatUint(c.MinimumDifficulty, 10)...)
	buf = append(buf, 0)
	buf = append(buf, strconv.FormatUint(c.ChainWindowSize, 10)...)
	buf = append(buf, 0)
	buf = append(buf, strconv.FormatUint(c.UnclePenalty, 10)...)
	buf = append(buf, 0)

	return randomx.ConsensusHash(buf)
}

// ApplyUnclePenalty Applies UnclePenalty efficiently
func (c *Consensus) ApplyUnclePenalty(weight types.Difficulty) (uncleWeight, unclePenalty types.Difficulty) {
	unclePenalty = weight.Mul64(c.UnclePenalty).Div64(100)
	uncleWeight = weight.Sub(unclePenalty)
	return
}

var ConsensusDefault = &Consensus{
	NetworkType:       NetworkMainnet,
	PoolName:          "mainnet test 2",
	TargetBlockTime:   10,
	MinimumDifficulty: 100000,
	ChainWindowSize:   2160,
	UnclePenalty:      20,
	HardForks:         p2poolMainNetHardForks,
	Id:                types.Hash{34, 175, 126, 231, 181, 11, 104, 146, 227, 153, 218, 107, 44, 108, 68, 39, 178, 81, 4, 212, 169, 4, 142, 0, 177, 110, 157, 240, 68, 7, 249, 24},
}

var ConsensusMini = &Consensus{
	NetworkType:       NetworkMainnet,
	PoolName:          "mini",
	TargetBlockTime:   10,
	MinimumDifficulty: 100000,
	ChainWindowSize:   2160,
	UnclePenalty:      20,
	HardForks:         p2poolMainNetHardForks,
	Id:                types.Hash{57, 130, 201, 26, 149, 174, 199, 250, 66, 80, 189, 18, 108, 216, 194, 220, 136, 23, 63, 24, 64, 113, 221, 44, 219, 86, 39, 163, 53, 24, 126, 196},
}

var ConsensusNano = &Consensus{
	NetworkType:       NetworkMainnet,
	PoolName:          "nano",
	TargetBlockTime:   30,
	MinimumDifficulty: 100000,
	ChainWindowSize:   2160,
	UnclePenalty:      10,
	HardForks:         p2poolMainNetHardForks,
	Id:                types.Hash{171, 248, 206, 148, 210, 226, 114, 99, 250, 145, 221, 96, 13, 216, 23, 63, 104, 53, 129, 168, 244, 80, 141, 138, 157, 250, 50, 54, 37, 189, 5, 89},
}
