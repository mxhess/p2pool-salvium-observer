package client

import (
	"context"
	"errors"
	"fmt"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/rpc"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/rpc/daemon"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/transaction"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"github.com/hashicorp/golang-lru/v2"
	"sync"
	"sync/atomic"
	"time"
)

var client atomic.Pointer[Client]

var lock sync.Mutex

var address = "http://localhost:18081"

func SetDefaultClientSettings(addr string) {
	if addr == "" {
		return
	}
	lock.Lock()
	defer lock.Unlock()
	address = addr
	client.Store(nil)
}

func GetDefaultClient() *Client {
	if c := client.Load(); c == nil {
		lock.Lock()
		defer lock.Unlock()
		if c = client.Load(); c == nil {
			//fallback for lock racing
			if c, err := NewClient(address); err != nil {
				utils.Panic(err)
			} else {
				client.Store(c)
				return c
			}
		}
		return c
	} else {
		return c
	}
}

// Client
type Client struct {
	c *rpc.Client
	d *daemon.Client

	coinbaseTransactionCache *lru.Cache[types.Hash, *transaction.CoinbaseTransaction]

	throttler <-chan time.Time
}

func NewClient(address string) (*Client, error) {
	c, err := rpc.NewClient(address)
	if err != nil {
		return nil, err
	}

	cache, err := lru.New[types.Hash, *transaction.CoinbaseTransaction](1024)
	if err != nil {
		return nil, err
	}

	return &Client{
		c:                        c,
		d:                        daemon.NewClient(c),
		coinbaseTransactionCache: cache,
		throttler:                time.Tick(time.Second / 8),
	}, nil
}

func (c *Client) SetThrottle(timesPerSecond uint64) {
	c.throttler = time.Tick(time.Second / time.Duration(timesPerSecond))
}

func (c *Client) GetTransactions(txIds ...types.Hash) (data [][]byte, jsonTx []*daemon.TransactionJSON, err error) {
	const restrictedTxCount = 100
	if len(txIds) > restrictedTxCount {
		i := txIds
		for {
			if len(i) > restrictedTxCount {
				d, j, err := c.GetTransactions(i[:restrictedTxCount]...)
				if err != nil {
					return nil, nil, err
				}
				data = append(data, d...)
				jsonTx = append(jsonTx, j...)
				i = i[restrictedTxCount:]
			} else {
				d, j, err := c.GetTransactions(i...)
				if err != nil {
					return nil, nil, err
				}
				data = append(data, d...)
				jsonTx = append(jsonTx, j...)
				i = i[:0]
				break
			}
		}
		return data, jsonTx, nil
	}
	<-c.throttler
	if result, err := c.d.GetTransactions(context.Background(), txIds); err != nil {
		return nil, nil, err
	} else {
		if len(result.Txs) != len(txIds) {
			return nil, nil, errors.New("invalid transaction count")
		}

		if jsonTxs, err := result.GetTransactions(); err != nil {
			return nil, nil, err
		} else {
			jsonTx = jsonTxs
		}

		for _, tx := range result.Txs {
			if len(tx.AsHex) > 0 {
				data = append(data, tx.AsHex)
			} else if len(tx.PrunableAsHex) > 0 {
				data = append(data, tx.PrunableAsHex)
			} else {
				data = append(data, tx.PrunedAsHex)
			}
		}
	}

	return data, jsonTx, nil
}

func (c *Client) GetCoinbaseTransaction(txId types.Hash) (*transaction.CoinbaseTransaction, error) {
	if tx, ok := c.coinbaseTransactionCache.Get(txId); !ok || tx == nil {
		<-c.throttler
		if result, err := c.d.GetTransactions(context.Background(), []types.Hash{txId}); err != nil {
			return nil, err
		} else {
			if len(result.Txs) != 1 {
				return nil, errors.New("invalid transaction count")
			}

			tx := &transaction.CoinbaseTransaction{}
			if err = tx.UnmarshalBinary(result.Txs[0].PrunedAsHex, false, false); err != nil {
				return nil, err
			}

			if tx.CalculateId() != txId {
				return nil, fmt.Errorf("expected %s, got %s", txId.String(), tx.CalculateId().String())
			}

			c.coinbaseTransactionCache.Add(txId, tx)

			return tx, nil
		}
	} else {
		return tx, nil
	}
}

type TransactionInputResult struct {
	Id         types.Hash         `json:"id"`
	UnlockTime uint64             `json:"unlock_time"`
	Inputs     []TransactionInput `json:"inputs"`
}

type TransactionInput struct {
	Amount     uint64     `json:"amount"`
	KeyOffsets []uint64   `json:"key_offsets"`
	KeyImage   types.Hash `json:"key_image"`
}

// GetTransactionInputs get transaction input information for several transactions, including key images and global key offsets
func (c *Client) GetTransactionInputs(ctx context.Context, hashes ...types.Hash) ([]TransactionInputResult, error) {
	<-c.throttler

	if result, err := c.d.GetTransactions(ctx, hashes); err != nil {
		return nil, err
	} else {
		if len(result.Txs) != len(hashes) {
			return nil, errors.New("invalid transaction count")
		}

		s := make([]TransactionInputResult, len(result.Txs))

		jsonTxs, err := result.GetTransactions()
		if err != nil {
			return nil, err
		}
		for ix, tx := range jsonTxs {
			s[ix].Id = hashes[ix]
			s[ix].UnlockTime = uint64(tx.UnlockTime)

			s[ix].Inputs = make([]TransactionInput, len(tx.Vin))
			for i, input := range tx.Vin {
				s[ix].Inputs[i].Amount = uint64(input.Key.Amount)
				s[ix].Inputs[i].KeyImage = input.Key.KImage
				s[ix].Inputs[i].KeyOffsets = make([]uint64, len(input.Key.KeyOffsets))
				for j, o := range input.Key.KeyOffsets {
					s[ix].Inputs[i].KeyOffsets[j] = uint64(o)
					if j > 0 {
						s[ix].Inputs[i].KeyOffsets[j] += s[ix].Inputs[i].KeyOffsets[j-1]
					}
				}
			}
		}

		return s, nil
	}
}

type Output struct {
	GlobalOutputIndex uint64     `json:"global_output_index"`
	Height            uint64     `json:"height"`
	Timestamp         uint64     `json:"timestamp"`
	Key               types.Hash `json:"key"`
	Mask              types.Hash `json:"mask"`
	TransactionId     types.Hash `json:"tx_id"`
	Unlocked          bool       `json:"unlocked"`
}

// GetOutputIndexes Get global output indexes for a given transaction
func (c *Client) GetOutputIndexes(id types.Hash) (indexes []uint64, err error) {
	<-c.throttler

	return c.d.GetOIndexes(context.Background(), id)
}

func (c *Client) GetOuts(inputs ...uint64) ([]Output, error) {
	<-c.throttler

	if result, err := c.d.GetOuts(context.Background(), func() []uint {
		r := make([]uint, len(inputs))
		for i, v := range inputs {
			r[i] = uint(v)
		}
		return r
	}(), true); err != nil {
		return nil, err
	} else {
		if len(result.Outs) != len(inputs) {
			return nil, errors.New("invalid output count")
		}

		s := make([]Output, len(inputs))
		for i := range result.Outs {
			o := &result.Outs[i]
			s[i].GlobalOutputIndex = inputs[i]
			s[i].Height = o.Height
			s[i].Key = o.Key
			s[i].Mask = o.Mask
			s[i].TransactionId = o.Txid
			s[i].Unlocked = o.Unlocked
		}

		return s, nil
	}
}

func (c *Client) GetVersion() (*daemon.GetVersionResult, error) {
	<-c.throttler
	if result, err := c.d.GetVersion(context.Background()); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetPeerList() (*daemon.GetPeerListResult, error) {
	<-c.throttler
	if result, err := c.d.GetPeerList(context.Background()); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetInfo() (*daemon.GetInfoResult, error) {
	<-c.throttler
	if result, err := c.d.GetInfo(context.Background()); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetBlockHeaderByHash(hash types.Hash, ctx context.Context) (*daemon.BlockHeader, error) {
	<-c.throttler
	if result, err := c.d.GetBlockHeaderByHash(ctx, []types.Hash{hash}); err != nil {
		return nil, err
	} else if result != nil && len(result.BlockHeaders) > 0 {
		return &result.BlockHeaders[0], nil
	} else {
		return nil, errors.New("not found")
	}
}

func (c *Client) GetBlock(hash types.Hash, ctx context.Context) (*daemon.GetBlockResult, error) {
	<-c.throttler
	if result, err := c.d.GetBlock(ctx, daemon.GetBlockRequestParameters{
		Hash: hash,
	}); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetBlockByHeight(height uint64, ctx context.Context) (*daemon.GetBlockResult, error) {
	<-c.throttler
	if result, err := c.d.GetBlock(ctx, daemon.GetBlockRequestParameters{
		Height: height,
	}); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetLastBlockHeader() (*daemon.GetLastBlockHeaderResult, error) {
	<-c.throttler
	if result, err := c.d.GetLastBlockHeader(context.Background()); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetBlockHeaderByHeight(height uint64, ctx context.Context) (*daemon.GetBlockHeaderByHeightResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.throttler:
		if result, err := c.d.GetBlockHeaderByHeight(ctx, height); err != nil {
			return nil, err
		} else {
			return result, nil
		}
	}
}

func (c *Client) GetBlockHeadersRangeResult(start, end uint64, ctx context.Context) (*daemon.GetBlockHeadersRangeResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.throttler:
		if result, err := c.d.GetBlockHeadersRange(ctx, start, end); err != nil {
			return nil, err
		} else {
			return result, nil
		}
	}
}

func (c *Client) SubmitBlock(blob []byte) (*daemon.SubmitBlockResult, error) {
	if result, err := c.d.SubmitBlock(context.Background(), blob); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetMinerData() (*daemon.GetMinerDataResult, error) {
	<-c.throttler
	if result, err := c.d.GetMinerData(context.Background()); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}

func (c *Client) GetBlockTemplate(address string) (*daemon.GetBlockTemplateResult, error) {
	<-c.throttler
	if result, err := c.d.GetBlockTemplate(context.Background(), address, 60); err != nil {
		return nil, err
	} else {
		return result, nil
	}
}
