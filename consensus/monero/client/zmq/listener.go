package zmq

import (
	"fmt"
	"maps"
	"slices"

	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

type Listeners map[Topic]func(gson []byte) error

func (l Listeners) Topics() []Topic {
	return slices.Sorted(maps.Keys(l))
}

func DecoderCallback[T any](cb func(T)) func(gson []byte) error {
	return func(gson []byte) error {
		var v T
		if err := utils.UnmarshalJSON(gson, &v); err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}
		cb(v)
		return nil
	}
}

func DecoderFullChainMain(cb func([]FullChainMain)) func(gson []byte) error {
	return DecoderCallback(cb)
}

func DecoderFullTxPoolAdd(cb func([]FullTxPoolAdd)) func(gson []byte) error {
	return DecoderCallback(cb)
}

func DecoderFullMinerData(cb func(*FullMinerData)) func(gson []byte) error {
	return DecoderCallback(cb)
}

func DecoderMinimalChainMain(cb func(*MinimalChainMain)) func(gson []byte) error {
	return DecoderCallback(cb)
}

func DecoderMinimalTxPoolAdd(cb func(mempool.Mempool)) func(gson []byte) error {
	return DecoderCallback(cb)
}
