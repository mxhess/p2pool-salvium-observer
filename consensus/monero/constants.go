package monero

// Network types - Legacy compatibility bytes
// These are used for BaseNetwork() comparisons
const (
	MainNetwork  uint8 = 0x18 // Base network identifier
	TestNetwork  uint8 = 0xbe 
	StageNetwork uint8 = 0x9e 
	
	// Legacy compatibility for old code
	IntegratedMainNetwork  uint8 = 0x19
	IntegratedTestNetwork  uint8 = 0xbf
	IntegratedStageNetwork uint8 = 0x9f
	
	SubAddressMainNetwork  uint8 = 0x2a
	SubAddressTestNetwork  uint8 = 0xd0
	SubAddressStageNetwork uint8 = 0xb0
)

// Salvium address prefixes (full varint values)
const (
	// Legacy SaLv mainnet
	SalviumMainAddress       uint64 = 0x3ef318
	SalviumMainIntegrated    uint64 = 0x55ef318
	SalviumMainSubaddress    uint64 = 0xf5ef318
	
	// Legacy SaLv testnet
	SalviumTestAddress       uint64 = 0x15beb318
	SalviumTestIntegrated    uint64 = 0xd055eb318
	SalviumTestSubaddress    uint64 = 0xa59eb318
	
	// Legacy SaLv stagenet
	SalviumStageAddress      uint64 = 0x149eb318
	SalviumStageIntegrated   uint64 = 0xf343eb318
	SalviumStageSubaddress   uint64 = 0x2d47eb318
	
	// Carrot SC1 mainnet
	CarrotMainAddress        uint64 = 0x180c96
	CarrotMainIntegrated     uint64 = 0x2ccc96
	CarrotMainSubaddress     uint64 = 0x314c96
	
	// Carrot SC1 testnet
	CarrotTestAddress        uint64 = 0x254c96
	CarrotTestIntegrated     uint64 = 0x1ac50c96
	CarrotTestSubaddress     uint64 = 0x3c54c96
	
	// Carrot SC1 stagenet
	CarrotStageAddress       uint64 = 0x24cc96
	CarrotStageIntegrated    uint64 = 0x1a848c96
	CarrotStageSubaddress    uint64 = 0x384cc96
)

// Transaction versions
const (
	TxVersion2Outs   = 2
	TxVersionNOuts   = 3
	TxVersionCarrot  = 4
	CurrentTxVersion = TxVersionCarrot
)

// Block unlock times
const (
	MinerRewardUnlockTime = 60  // CRYPTONOTE_MINED_MONEY_UNLOCK_WINDOW
	TxSpendableAge        = 10  // CRYPTONOTE_DEFAULT_TX_SPENDABLE_AGE
)

// Emission parameters
const (
	// FINAL_SUBSIDY_PER_MINUTE = 30000000 (3 * 10^7)
	// Block time = 120 seconds = 2 minutes
	// So tail emission per block = 30000000 * 2 = 60000000
	TailEmissionReward = 60000000
	
	MoneySupply           = 18440000000000000 // 184.4M coins * 10^8
	EmissionSpeedFactor   = 21
	DifficultyTarget      = 120 // seconds per block
)

// Block timing
const (
	BlockTime = DifficultyTarget // 120 seconds per block
)

// Transaction unlock time
const (
	TransactionUnlockTime = TxSpendableAge // 10 blocks
)
