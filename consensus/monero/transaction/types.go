package transaction

// TransactionType indicates the type of Salvium transaction
type TransactionType uint8

const (
	TxTypeUnset    TransactionType = 0
	TxTypeProtocol TransactionType = 1
	TxTypeMiner    TransactionType = 2
	TxTypeTransfer TransactionType = 3
	TxTypeStake    TransactionType = 4
	TxTypeConvert  TransactionType = 5
)

func (t TransactionType) String() string {
	switch t {
	case TxTypeUnset:
		return "unset"
	case TxTypeProtocol:
		return "protocol"
	case TxTypeMiner:
		return "miner"
	case TxTypeTransfer:
		return "transfer"
	case TxTypeStake:
		return "stake"
	case TxTypeConvert:
		return "convert"
	default:
		return "unknown"
	}
}

// ProtocolTxData contains return information for protocol transactions
type ProtocolTxData struct {
	ReturnAddress    [32]byte `json:"return_address"`
	ReturnPubkey     [32]byte `json:"return_pubkey"`
	ReturnViewTag    [1]byte  `json:"return_view_tag"`
	ReturnAnchorEnc  []byte   `json:"return_anchor_enc"`
}
