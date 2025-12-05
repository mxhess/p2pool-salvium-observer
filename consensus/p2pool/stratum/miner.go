package stratum

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/address"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/sidechain"
	gojson "git.gammaspectra.live/P2Pool/go-json"
)

type MinerTrackingEntry struct {
	Lock         sync.RWMutex
	Counter      atomic.Uint64
	LastTemplate atomic.Uint64
	Templates    map[uint64]*Template
	LastJob      time.Time
}

type Client struct {
	Lock       sync.RWMutex
	Conn       *net.TCPConn
	encoder    *gojson.Encoder
	decoder    *gojson.Decoder
	Agent      string
	Login      bool
	Extensions struct {
		Algo bool
	}
	MergeMiningExtra  sidechain.MergeMiningExtra
	Address           address.PackedAddress
	SubaddressViewPub [33]byte
	Password          string
	RigId             string
	buf               []byte
	RpcId             uint32
}

func (c *Client) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(time.Second * 5)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
