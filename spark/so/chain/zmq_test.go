package chain

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pebbe/zmq4"
	"github.com/stretchr/testify/require"
)

type ZmqTestPublisher struct {
	ctx    *zmq4.Context
	socket *zmq4.Socket
	t      *testing.T
}

// NewZmqTestPublisher creates and binds a PUB socket to an address.
func NewZmqTestPublisher(t *testing.T, address string) (*ZmqTestPublisher, error) {
	ctx, err := zmq4.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to create ZMQ context: %w", err)
	}

	pub, err := ctx.NewSocket(zmq4.PUB)
	if err != nil {
		return nil, fmt.Errorf("failed to create PUB socket: %w", err)
	}

	if err := pub.Bind(address); err != nil {
		return nil, fmt.Errorf("failed to bind PUB socket: %w", err)
	}

	return &ZmqTestPublisher{
		ctx:    ctx,
		socket: pub,
		t:      t,
	}, nil
}

// SendMessage sends a message with the specified topic and message body.
func (p *ZmqTestPublisher) SendMessage(topic string, message string) error {
	p.t.Logf("[zmqpub] Sending message: %s %s", topic, message)

	res, err := p.socket.SendMessage(topic, message)

	if err != nil {
		p.t.Logf("[zmqpub] Failed to send message: %v", err)
	} else {
		p.t.Logf("[zmqpub] Message sent (res: %d)", res)
	}

	return err
}

// Close closes the PUB socket and terminates the ZMQ context. This should be called as defer
// after creating a new ZmqTestPublisher.
func (p *ZmqTestPublisher) Close() {
	if err := p.socket.Close(); err != nil {
		p.t.Errorf("[zmqpub] Failed to close ZMQ socket: %v", err)
	}

	if err := p.ctx.Term(); err != nil {
		p.t.Errorf("[zmqpub] Failed to terminate ZMQ context: %v", err)
	}
}

func TestZmqSetupTeardown(t *testing.T) {
	zmqPub, err := NewZmqTestPublisher(t, "tcp://127.0.0.1:5555")
	require.NoError(t, err, "Failed to create ZMQ publisher")
	defer zmqPub.Close() //nolint:errcheck

	zmqSub, err := NewZmqSubscriber()
	require.NoError(t, err, "Failed to create ZMQ subscriber")

	zmqSub.Close() //nolint:errcheck
}

func TestZmqSubscribe(t *testing.T) {
	// t.Skip("Flaky (LPT-352)")
	zmqPub, err := NewZmqTestPublisher(t, "tcp://127.0.0.1:5555")
	require.NoError(t, err, "Failed to create ZMQ publisher")
	defer zmqPub.Close() //nolint:errcheck

	zmqSub, err := NewZmqSubscriber()
	require.NoError(t, err, "Failed to create ZMQ subscriber: %v")
	defer zmqSub.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subscribeChan, errChan, err := zmqSub.Subscribe(ctx, "tcp://127.0.0.1:5555", "rawblock")
	require.NoError(t, err, "Failed to subscribe to ZMQ subscriber")

	// Send a message to the subscriber and verify that it is received. Give this a few tries because
	// ZMQ can take a moment to establish the connection, and the subscriber won't receive messages
	// until the connection is established.
	attempts := 0
	received := false
	for attempts < 5 && !received {
		attempts++

		err = zmqPub.SendMessage("rawblock", "TESTTESTTEST")
		require.NoError(t, err, "Failed to send message through ZMQ publisher")

		t.Logf("Waiting for message (attempt %d / 5)...", attempts)
		select {
		case <-subscribeChan:
			received = true
		case err := <-errChan:
			require.NoError(t, err, "Failed to receive message")
		case <-time.After(200 * time.Millisecond):
			t.Logf("Failed to receive message after 200ms, retrying...")
		}

		attempts++
	}

	if !received {
		t.Fatal("Failed to receive ZMQ message after 5 attempts")
	}
}
