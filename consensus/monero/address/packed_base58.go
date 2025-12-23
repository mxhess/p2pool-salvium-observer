//go:build packedaddress_base58

package address

func (p PackedAddress) String() string {
	return string(p.ToBase58(PackedAddressGlobalNetwork))
}

func (p PackedAddress) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 150) // Carrot addresses can be up to 143 chars + 2 quotes
	buf = append(buf, '"')
	buf = append(buf, p.ToBase58(PackedAddressGlobalNetwork)...)
	buf = append(buf, '"')
	return buf, nil
}

func (p *PackedAddress) UnmarshalJSON(b []byte) error {
	var a Address
	err := a.UnmarshalJSON(b)
	if err != nil {
		return err
	}
	*p = a.ToPackedAddress()
	return nil
}
