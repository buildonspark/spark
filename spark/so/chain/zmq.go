package chain

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common/logging"
	"github.com/pebbe/zmq4"
	"go.uber.org/zap"
)

// BlockNotification identifies the block announced by a ZMQ rawblock
// publication. Parsed is false when the payload lacks a decodable block with
// a BIP34 coinbase height; the notification then carries no target identity.
type BlockNotification struct {
	Hash   chainhash.Hash
	Height int64
	Parsed bool
}

// parseBlockNotification extracts the announced block's hash (header) and
// height (BIP34 coinbase) with no RPC round trips; the payload's remaining
// megabytes of transactions are deliberately not deserialized.
func parseBlockNotification(payload []byte) BlockNotification {
	r := bytes.NewReader(payload)
	var header wire.BlockHeader
	if err := header.Deserialize(r); err != nil {
		return BlockNotification{}
	}
	if _, err := wire.ReadVarInt(r, 0); err != nil {
		return BlockNotification{}
	}
	var coinbase wire.MsgTx
	if err := coinbase.Deserialize(r); err != nil {
		return BlockNotification{}
	}
	height, err := blockchain.ExtractCoinbaseHeight(btcutil.NewTx(&coinbase))
	if err != nil {
		return BlockNotification{}
	}
	return BlockNotification{Hash: header.BlockHash(), Height: int64(height), Parsed: true}
}

type ZmqSubscriber struct {
	ctx *zmq4.Context
}

// NewZmqSubscriber creates a new ZMQ subscriber that connects to the specified endpoint and sets
// the filter. The filter is used to subscribe to specific topics.
func NewZmqSubscriber() (*ZmqSubscriber, error) {
	zmqCtx, err := zmq4.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to create ZMQ context: %w", err)
	}

	return &ZmqSubscriber{ctx: zmqCtx}, nil
}

// Subscribe starts receiving messages from the ZMQ socket. Each received message is parsed into
// a BlockNotification identifying the announced block when possible; unparseable payloads yield
// a zero BlockNotification (Parsed=false) that still signals a message arrived.
//
// The returned channels are closed when one of the following happens:
//  1. The context is cancelled.
//  2. The Close() method is called on the ZmqSubscriber.
//  3. An error occurs while receiving messages from the socket. In this case, the error will be
//     sent to the returned error channel.
//
// Calling `Subscribe` multiple times with the same endpoint & filter will result in undefined
// behavior, do not do this!
func (z *ZmqSubscriber) Subscribe(ctx context.Context, endpoint string, filter string) (<-chan BlockNotification, <-chan error, error) {
	zmqSocket, err := z.ctx.NewSocket(zmq4.SUB)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create ZMQ subscriber socket: %w", err)
	}

	// Until the receive goroutine below takes ownership, every error path must close the
	// socket — a leaked socket blocks Close()'s ctx.Term() forever.
	closeAndErr := func(err error) (<-chan BlockNotification, <-chan error, error) {
		if closeErr := zmqSocket.Close(); closeErr != nil {
			err = fmt.Errorf("%w (also failed to close socket: %w)", err, closeErr)
		}
		return nil, nil, err
	}

	// A half-open TCP connection (e.g. a silently dropped conntrack flow) leaves RecvMessage
	// blocked forever with no error. ZMTP heartbeats let libzmq detect the dead peer and
	// transparently reconnect.
	if err := configureHeartbeats(zmqSocket); err != nil {
		return closeAndErr(fmt.Errorf("failed to configure ZMQ heartbeats: %w", err))
	}

	if err := zmqSocket.Connect(endpoint); err != nil {
		return closeAndErr(fmt.Errorf("failed to connect to ZMQ endpoint %s: %w", endpoint, err))
	}

	if err := zmqSocket.SetSubscribe(filter); err != nil {
		return closeAndErr(fmt.Errorf("failed to set ZMQ subscription filter %s: %w", filter, err))
	}

	logger := logging.GetLoggerFromContext(ctx).With(zap.String("subscription", filter))

	msgChan := make(chan BlockNotification, 10)
	errChan := make(chan error)

	go func() {
		defer func() {
			logger.Info("[zmq] Closing subscriber socket...")
			if err := zmqSocket.Close(); err != nil {
				logger.Error("[zmq] Failed to close subscriber socket", zap.Error(err))
			}
			logger.Info("[zmq] Subscriber socket closed")
		}()
		defer close(msgChan)
		defer close(errChan)

		logger.Info("[zmq] Starting message receive loop...")

		for {
			select {
			case <-ctx.Done():
				return
			default:
				logger.Info("[zmq] Waiting for message...")
				msg, err := zmqSocket.RecvMessageBytes(0)
				if err != nil {
					if zmq4.AsErrno(err) != zmq4.ETERM {
						logger.Error("[zmq] Failed to receive message", zap.Error(err))

						select {
						case errChan <- fmt.Errorf("failed to receive message: %w", err):
						default:
							logger.Warn("[zmq] No receiver for error channel, dropping error...", zap.Error(err))
						}
					}

					return
				}

				logger.Info("[zmq] Message received!")
				// Message parts are [topic, payload, sequence].
				notification := BlockNotification{}
				if len(msg) >= 2 {
					notification = parseBlockNotification(msg[1])
				}
				select {
				case msgChan <- notification:
				case <-time.After(5 * time.Second):
					logger.Warn("[zmq] Message channel is full, dropping message...")
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return msgChan, errChan, nil
}

func configureHeartbeats(socket *zmq4.Socket) error {
	if err := socket.SetHeartbeatIvl(30 * time.Second); err != nil {
		return err
	}
	return socket.SetHeartbeatTimeout(30 * time.Second)
}

// Close closes the ZMQ socket and terminates the context. This should be called when the subscriber
// is no longer needed.
func (z *ZmqSubscriber) Close() error {
	// This will block until all sockets are closed, so we must make sure to handle `zmq4.ETERM` in
	// our sockets and make sure they are closed in response to it!
	return z.ctx.Term()
}
