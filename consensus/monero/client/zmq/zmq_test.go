package zmq_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/zmq"
	"git.gammaspectra.live/P2Pool/consensus/v4/p2pool/mempool"
)

func TestJSONFromFrame(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		input         []byte
		expectedJSON  []byte
		expectedTopic zmq.Topic
		err           string
	}{
		{
			name:  "nil",
			input: nil,
			err:   "malformed",
		},

		{
			name:  "empty",
			input: []byte{},
			err:   "malformed",
		},

		{
			name:  "unknown-topic",
			input: []byte(`foobar:[{"foo":"bar"}]`),
			err:   "unknown topic",
		},

		{
			name:          "proper w/ known-topic",
			input:         []byte(`json-minimal-txpool_add:[{"foo":"bar"}]`),
			expectedTopic: zmq.TopicMinimalTxPoolAdd,
			expectedJSON:  []byte(`[{"foo":"bar"}]`),
		},
	} {
		tc := tc

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			aTopic, aJSON, err := zmq.JSONFromFrame([]zmq.Topic{tc.expectedTopic}, tc.input)
			if tc.err != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tc.err) {
					t.Errorf("expected %s in, got %s", tc.err, err.Error())
				}
				return
			}

			if err != nil {
				t.Errorf("expected no error, got %s", err)
			}

			if tc.expectedTopic != aTopic {
				t.Errorf("expected %s, got %s", tc.expectedTopic, aTopic)
			}

			if bytes.Compare(tc.expectedJSON, aJSON) != 0 {
				t.Errorf("expected %s, got %s", string(tc.expectedJSON), string(aJSON))
			}
		})
	}
}

func TestClient(t *testing.T) {
	client := zmq.NewClient(os.Getenv("MONEROD_ZMQ_URL"))
	ctx, ctxFunc := context.WithTimeout(context.Background(), time.Second*10)
	defer ctxFunc()
	err := client.Listen(ctx, zmq.Listeners{
		zmq.TopicFullChainMain: zmq.DecoderFullChainMain(func(mains []zmq.FullChainMain) {
			for _, chainMain := range mains {
				t.Log(chainMain)
			}
		}),
		zmq.TopicFullTxPoolAdd: zmq.DecoderFullTxPoolAdd(func(txs []zmq.FullTxPoolAdd) {
			t.Log(txs)
		}),
		zmq.TopicFullMinerData: zmq.DecoderFullMinerData(func(data *zmq.FullMinerData) {
			t.Log(data)
		}),
		zmq.TopicMinimalChainMain: zmq.DecoderMinimalChainMain(func(main *zmq.MinimalChainMain) {
			t.Log(main)
		}),
		zmq.TopicMinimalTxPoolAdd: zmq.DecoderMinimalTxPoolAdd(func(txs mempool.Mempool) {
			t.Log(txs)
		}),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
