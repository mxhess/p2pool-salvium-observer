package block

import (
	"encoding/binary"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

// HF_VERSION_ENABLE_ORACLE - hard fork version when pricing records are enabled
const HFVersionEnableOracle = 255

// SupplyData contains supply information for SAL and VSD
type SupplyData struct {
	Sal uint64 `json:"sal"`
	Vsd uint64 `json:"vsd"`
}

func (s *SupplyData) BufferLength() int {
	return utils.UVarInt64Size(s.Sal) + utils.UVarInt64Size(s.Vsd)
}

func (s *SupplyData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 0, s.BufferLength())
	buf = binary.AppendUvarint(buf, s.Sal)
	buf = binary.AppendUvarint(buf, s.Vsd)
	return buf, nil
}

func (s *SupplyData) UnmarshalBinary(data []byte) error {
	var err error
	var n int
	
	s.Sal, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	s.Vsd, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	
	return err
}

// AssetData contains pricing information for a single asset
type AssetData struct {
	AssetType string `json:"asset_type"`
	SpotPrice uint64 `json:"spot_price"`
	MaPrice   uint64 `json:"ma_price"`
}

func (a *AssetData) BufferLength() int {
	return utils.UVarInt64Size(len(a.AssetType)) + len(a.AssetType) +
		utils.UVarInt64Size(a.SpotPrice) +
		utils.UVarInt64Size(a.MaPrice)
}

func (a *AssetData) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 0, a.BufferLength())
	buf = binary.AppendUvarint(buf, uint64(len(a.AssetType)))
	buf = append(buf, []byte(a.AssetType)...)
	buf = binary.AppendUvarint(buf, a.SpotPrice)
	buf = binary.AppendUvarint(buf, a.MaPrice)
	return buf, nil
}

func (a *AssetData) UnmarshalBinary(data []byte) error {
	var n int
	
	length, n := binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	if uint64(len(data)) < length {
		return ErrInvalidLength
	}
	a.AssetType = string(data[:length])
	data = data[length:]
	
	a.SpotPrice, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	a.MaPrice, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	
	return nil
}

// PricingRecord contains oracle pricing data for the block
type PricingRecord struct {
	PrVersion uint64       `json:"pr_version"`
	Height    uint64       `json:"height"`
	Supply    SupplyData   `json:"supply"`
	Assets    []AssetData  `json:"assets"`
	Timestamp uint64       `json:"timestamp"`
	Signature []byte       `json:"signature"`
}

func (p *PricingRecord) Empty() bool {
	return p.PrVersion == 0 && p.Height == 0 && len(p.Assets) == 0
}

func (p *PricingRecord) BufferLength() int {
	length := utils.UVarInt64Size(p.PrVersion) +
		utils.UVarInt64Size(p.Height) +
		p.Supply.BufferLength() +
		utils.UVarInt64Size(len(p.Assets)) +
		utils.UVarInt64Size(p.Timestamp) +
		utils.UVarInt64Size(len(p.Signature)) +
		len(p.Signature)
	
	for _, asset := range p.Assets {
		length += asset.BufferLength()
	}
	
	return length
}

func (p *PricingRecord) MarshalBinary() ([]byte, error) {
	buf := make([]byte, 0, p.BufferLength())
	buf = binary.AppendUvarint(buf, p.PrVersion)
	buf = binary.AppendUvarint(buf, p.Height)
	
	supplyData, err := p.Supply.MarshalBinary()
	if err != nil {
		return nil, err
	}
	buf = append(buf, supplyData...)
	
	buf = binary.AppendUvarint(buf, uint64(len(p.Assets)))
	for _, asset := range p.Assets {
		assetData, err := asset.MarshalBinary()
		if err != nil {
			return nil, err
		}
		buf = append(buf, assetData...)
	}
	
	buf = binary.AppendUvarint(buf, p.Timestamp)
	buf = binary.AppendUvarint(buf, uint64(len(p.Signature)))
	buf = append(buf, p.Signature...)
	
	return buf, nil
}

func (p *PricingRecord) UnmarshalBinary(data []byte) error {
	var n int
	
	p.PrVersion, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	p.Height, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	if err := p.Supply.UnmarshalBinary(data); err != nil {
		return err
	}
	data = data[p.Supply.BufferLength():]
	
	assetCount, n := binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	p.Assets = make([]AssetData, assetCount)
	for i := uint64(0); i < assetCount; i++ {
		if err := p.Assets[i].UnmarshalBinary(data); err != nil {
			return err
		}
		data = data[p.Assets[i].BufferLength():]
	}
	
	p.Timestamp, n = binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	sigLength, n := binary.Uvarint(data)
	if n <= 0 {
		return ErrInvalidVarint
	}
	data = data[n:]
	
	if uint64(len(data)) < sigLength {
		return ErrInvalidLength
	}
	p.Signature = make([]byte, sigLength)
	copy(p.Signature, data[:sigLength])
	
	return nil
}
