package zmq

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"git.gammaspectra.live/P2Pool/zmq4"
)

type Client struct {
	endpoint string
	sub      zmq4.Socket
}

// NewClient instantiates a new client that will receive monerod's zmq events.
//
//   - `topics` is a list of fully-formed zmq topic to subscribe to
//
//   - `endpoint` is the full address where monerod has been configured to
//     publish the messages to, including the network schama. for instance,
//     considering that monerod has been started with
//
//     monerod --zmq-pub tcp://127.0.0.1:18085
//
//     `endpoint` should be 'tcp://127.0.0.1:18085'.
func NewClient(endpoint string) *Client {
	return &Client{
		endpoint: endpoint,
	}
}

// Listen listens for a list of topics for this client (via NewClient).
func (c *Client) Listen(ctx context.Context, listeners Listeners) error {
	topics := listeners.Topics()
	if err := c.listen(ctx, topics...); err != nil {
		return fmt.Errorf("listen on '%s': %w", strings.Join(func() (r []string) {
			for _, s := range topics {
				r = append(r, string(s))
			}
			return r
		}(), ", "), err)
	}

	if err := c.loop(listeners); err != nil {
		return fmt.Errorf("loop: %w", err)
	}

	return nil
}

// Close closes any established connection, if any.
func (c *Client) Close() error {
	if c.sub == nil {
		return nil
	}

	return c.sub.Close()
}

func (c *Client) listen(ctx context.Context, topics ...Topic) error {
	c.sub = zmq4.NewSub(ctx)

	err := c.sub.Dial(c.endpoint)
	if err != nil {
		return fmt.Errorf("dial '%s': %w", c.endpoint, err)
	}

	for _, topic := range topics {
		err = c.sub.SetOption(zmq4.OptionSubscribe, string(topic))
		if err != nil {
			return fmt.Errorf("subscribe: %w", err)
		}
	}

	return nil
}

func (c *Client) loop(listeners Listeners) error {
	topics := listeners.Topics()
	for {
		msg, err := c.sub.Recv()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		for _, frame := range msg.Frames {
			err := c.ingestFrameArray(topics, listeners, frame)
			if err != nil {
				return fmt.Errorf("consume frame: %w", err)
			}
		}
	}
}

func (c *Client) ingestFrameArray(topics []Topic, listeners Listeners, frame []byte) error {
	topic, gson, err := jsonFromFrame(topics, frame)
	if err != nil {
		return fmt.Errorf("json from frame: %w", err)
	}

	if callback, ok := listeners[topic]; !ok {
		return fmt.Errorf("topic '%s' doesn't match "+
			"expected any of '%s'", topic, strings.Join(func() (r []string) {
			for _, s := range topics {
				r = append(r, string(s))
			}
			return r
		}(), ", "))
	} else {
		return callback(gson)
	}
}

func jsonFromFrame(topics []Topic, frame []byte) (Topic, []byte, error) {
	unknown := TopicUnknown

	parts := bytes.SplitN(frame, []byte(":"), 2)
	if len(parts) != 2 {
		return unknown, nil, fmt.Errorf(
			"malformed: expected 2 parts, got %d", len(parts))
	}

	topic, gson := string(parts[0]), parts[1]

	if !slices.Contains(topics, Topic(topic)) {
		return unknown, nil, fmt.Errorf("unknown topic '%s'", topic)
	}

	return Topic(topic), gson, nil
}
