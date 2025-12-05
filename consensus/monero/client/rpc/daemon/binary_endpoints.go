package daemon

import (
	"bytes"
	"context"
	"errors"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/levin"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"io"
)

const (
	endpointGetOIndexes = "/get_o_indexes.bin"
)

func (c *Client) GetOIndexes(
	ctx context.Context, txid types.Hash,
) (indexes []uint64, finalError error) {

	storage := levin.PortableStorage{Entries: levin.Entries{
		levin.Entry{
			Name:         "txid",
			Serializable: levin.BoostString(txid[:]),
		},
	}}

	data, err := storage.Bytes()
	if err != nil {
		return nil, err
	}

	resp, err := c.RawBinaryRequest(ctx, endpointGetOIndexes, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	if buf, err := io.ReadAll(resp); err != nil {
		return nil, err
	} else {
		defer func() {
			if r := recover(); r != nil {
				indexes = nil
				finalError = errors.New("error decoding")
			}
		}()
		responseStorage, err := levin.NewPortableStorageFromBytes(buf)
		if err != nil {
			return nil, err
		}
		for _, e := range responseStorage.Entries {
			if e.Name == "o_indexes" {
				if entries, ok := e.Value.(levin.Entries); ok {
					indexes = make([]uint64, 0, len(entries))
					for _, e2 := range entries {
						if v, ok := e2.Value.(uint64); ok {
							indexes = append(indexes, v)
						}
					}
					return indexes, nil
				}
			}
		}
	}

	return nil, errors.New("could not get outputs")
}
