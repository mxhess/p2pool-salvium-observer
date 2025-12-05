package p2p

import (
	"encoding/binary"

	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
)

type MessageId uint8

// from p2p_server.h
const (
	MessageHandshakeChallenge = MessageId(iota)
	MessageHandshakeSolution
	MessageListenPort
	MessageBlockRequest
	MessageBlockResponse
	MessageBlockBroadcast
	MessagePeerListRequest
	MessagePeerListResponse
	// MessageBlockBroadcastCompact Protocol 1.1
	MessageBlockBroadcastCompact
	// MessageBlockNotify Protocol 1.2
	MessageBlockNotify
	// MessageAuxJobDonation Protocol 1.3
	// Donation messages are signed by author's private keys to prevent their abuse/misuse.
	MessageAuxJobDonation
	// MessageMoneroBlockBroadcast Protocol 1.4
	// Broadcast 3rd-party Monero blocks to make the whole Monero network faster
	MessageMoneroBlockBroadcast

	MessageInternal = 0xff
)

type InternalMessageId uint64

type MoneroBlockBroadcastHeader struct {
	HeaderSize           uint32
	MinerTransactionSize uint32
}

func (h *MoneroBlockBroadcastHeader) TotalSize() uint64 {
	return uint64(h.HeaderSize) + uint64(h.MinerTransactionSize)
}

func (h *MoneroBlockBroadcastHeader) BufferLength() int {
	return 4 * 2
}

func (h *MoneroBlockBroadcastHeader) MarshalBinary() (data []byte, err error) {
	return h.AppendBinary(make([]byte, 0, h.BufferLength()))
}

func (h *MoneroBlockBroadcastHeader) AppendBinary(preAllocatedBuf []byte) (data []byte, err error) {
	data = preAllocatedBuf
	data = binary.LittleEndian.AppendUint32(data, h.HeaderSize)
	data = binary.LittleEndian.AppendUint32(data, h.MinerTransactionSize)
	return data, nil
}

func (h *MoneroBlockBroadcastHeader) FromReader(reader utils.ReaderAndByteReader) (err error) {
	if err := binary.Read(reader, binary.LittleEndian, &h.HeaderSize); err != nil {
		return err
	}

	if err := binary.Read(reader, binary.LittleEndian, &h.MinerTransactionSize); err != nil {
		return err
	}

	return nil
}
