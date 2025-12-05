package merge_mining

import (
	"bytes"
	"errors"
	"fmt"
	"git.gammaspectra.live/P2Pool/consensus/v4/monero/crypto"
	"git.gammaspectra.live/P2Pool/consensus/v4/types"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"io"
	"net/http"
	"net/url"
)

type Client interface {
	GetChainId() (id types.Hash, err error)
	GetJob(chainAddress string, auxiliaryHash types.Hash, height uint64, prevId types.Hash) (job AuxiliaryJob, same bool, err error)
	SubmitSolution(job AuxiliaryJob, blob []byte, proof crypto.MerkleProof) (status string, err error)
}

type GenericClient struct {
	Address *url.URL
	Client  *http.Client
}

func NewGenericClient(address string, client *http.Client) (*GenericClient, error) {
	uri, err := url.Parse(address)
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = http.DefaultClient
	}

	return &GenericClient{
		Address: uri,
		Client:  client,
	}, nil
}

type RPCJSON struct {
	JSONRPC string `json:"jsonrpc"`
	Id      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type MergeMiningGetChainIdResult struct {
	Result struct {
		ChainID types.Hash `json:"chain_id"`
	} `json:"result"`
	Error string `json:"error"`
}

type MergeMiningGetJobJSON struct {
	// Address A wallet address on the merge mined chain
	Address string `json:"address"`

	// AuxiliaryHash Merge mining job that is currently being used
	AuxiliaryHash types.Hash `json:"aux_hash"`

	// Height Monero height
	Height uint64 `json:"height"`

	// PreviousId Hash of the previous Monero block
	PreviousId types.Hash `json:"prev_id"`
}

type MergeMiningGetJobResult struct {
	Result AuxiliaryJob `json:"result"`
	Error  string       `json:"error"`
}

type MergeMiningSubmitSolutionJSON struct {
	// AuxiliaryBlob blob of data returned by merge_mining_get_job.
	AuxiliaryBlob types.Bytes `json:"aux_blob"`

	// AuxiliaryHash A 32-byte hex-encoded hash of the aux_blob - the same value that was returned by merge_mining_get_job.
	AuxiliaryHash types.Hash `json:"aux_hash"`

	// Blob Monero block template that has enough PoW to satisfy difficulty returned by merge_mining_get_job.
	// It also must have a merge mining tag in tx_extra of the coinbase transaction.
	Blob types.Bytes `json:"blob"`

	// MerkleProof A proof that aux_hash was included when calculating Merkle root hash from the merge mining tag
	MerkleProof crypto.MerkleProof `json:"merkle_proof"`
}

type MergeMiningSubmitSolutionResult struct {
	Result struct {
		Status string `json:"status"`
	} `json:"result"`
	Error string `json:"error"`
}

func (c *GenericClient) GetChainId() (id types.Hash, err error) {
	data, err := utils.MarshalJSON(RPCJSON{
		JSONRPC: "2.0",
		Id:      "0",
		Method:  "merge_mining_get_chain_id",
	})
	if err != nil {
		return types.ZeroHash, err
	}

	response, err := c.Client.Do(&http.Request{
		Method: "POST",
		URL:    c.Address,
		Header: http.Header{
			"Content-Type": []string{"application/json-rpc"},
		},
		Body:          io.NopCloser(bytes.NewBuffer(data)),
		ContentLength: int64(len(data)),
	})
	if err != nil {
		return types.ZeroHash, err
	}
	defer io.ReadAll(response.Body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return types.ZeroHash, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	resultJSON, err := io.ReadAll(response.Body)
	if err != nil {
		return types.ZeroHash, err
	}

	var result MergeMiningGetChainIdResult
	if err := utils.UnmarshalJSON(resultJSON, &result); err != nil {
		return types.ZeroHash, err
	}

	if result.Error != "" {
		return types.ZeroHash, errors.New(result.Error)
	}

	return result.Result.ChainID, nil
}
func (c *GenericClient) GetJob(chainAddress string, auxiliaryHash types.Hash, height uint64, prevId types.Hash) (job AuxiliaryJob, same bool, err error) {
	data, err := utils.MarshalJSON(RPCJSON{
		JSONRPC: "2.0",
		Id:      "0",
		Method:  "merge_mining_get_job",
		Params: MergeMiningGetJobJSON{
			Address:       chainAddress,
			AuxiliaryHash: auxiliaryHash,
			Height:        height,
			PreviousId:    prevId,
		},
	})
	if err != nil {
		return AuxiliaryJob{}, false, err
	}

	response, err := c.Client.Do(&http.Request{
		Method: "POST",
		URL:    c.Address,
		Header: http.Header{
			"Content-Type": []string{"application/json-rpc"},
		},
		Body:          io.NopCloser(bytes.NewBuffer(data)),
		ContentLength: int64(len(data)),
	})
	if err != nil {
		return AuxiliaryJob{}, false, err
	}
	defer io.ReadAll(response.Body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AuxiliaryJob{}, false, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	resultJSON, err := io.ReadAll(response.Body)
	if err != nil {
		return AuxiliaryJob{}, false, err
	}

	var result MergeMiningGetJobResult
	if err := utils.UnmarshalJSON(resultJSON, &result); err != nil {
		return AuxiliaryJob{}, false, err
	}

	if result.Error != "" {
		return AuxiliaryJob{}, false, errors.New(result.Error)
	}

	// If aux_hash is the same as in the request, all other fields will be ignored by P2Pool, so they don't have to be included in the response.
	if result.Result.Hash == auxiliaryHash {
		return AuxiliaryJob{}, true, nil
	}

	// Moreover, empty response will be interpreted as a response having the same aux_hash as in the request. This enables an efficient polling.
	// TODO: properly check for emptiness
	if result.Result.Hash == types.ZeroHash {
		return AuxiliaryJob{}, true, nil
	}

	return result.Result, false, nil
}

func (c *GenericClient) SubmitSolution(job AuxiliaryJob, blob []byte, proof crypto.MerkleProof) (status string, err error) {
	data, err := utils.MarshalJSON(RPCJSON{
		JSONRPC: "2.0",
		Id:      "0",
		Method:  "merge_mining_submit_solution",
		Params: MergeMiningSubmitSolutionJSON{
			AuxiliaryBlob: job.Blob,
			AuxiliaryHash: job.Hash,
			Blob:          blob,
			MerkleProof:   proof,
		},
	})
	if err != nil {
		return "", err
	}

	response, err := c.Client.Do(&http.Request{
		Method: "POST",
		URL:    c.Address,
		Header: http.Header{
			"Content-Type": []string{"application/json-rpc"},
		},
		Body:          io.NopCloser(bytes.NewBuffer(data)),
		ContentLength: int64(len(data)),
	})
	if err != nil {
		return "", err
	}
	defer io.ReadAll(response.Body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	resultJSON, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	var result MergeMiningSubmitSolutionResult
	if err := utils.UnmarshalJSON(resultJSON, &result); err != nil {
		return "", err
	}

	if result.Error != "" {
		return "", errors.New(result.Error)
	}

	return result.Result.Status, nil
}
