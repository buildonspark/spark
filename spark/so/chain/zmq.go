package chain

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/logging"
	"github.com/pebbe/zmq4"
)

type ZmqSubscriber struct {
	ctx *zmq4.Context
}

// NewZmqSubscriber creates a new ZMQ subscriber that connects to the specified endpoint and sets
// the filter. The filter is used to subscribe to specific topics.
func NewZmqSubscriber() (*ZmqSubscriber, error) {
	zmqCtx, err := zmq4.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to create ZMQ context: %v", err)
	}

	return &ZmqSubscriber{ctx: zmqCtx}, nil
}

// Subscribe starts receiving messages from the ZMQ socket. Note that it does not return the message
// itself, it merely notifies the subscriber that a message has been received.
//
// The returned channels are closed when one of the following happens:
//  1. The context is cancelled.
//  2. The Close() method is called on the ZmqSubscriber.
//  3. An error occurs while receiving messages from the socket. In this case, the error will be
//     sent to the returned error channel.
//
// Calling `Subscribe` multiple times with the same endpoint & filter will result in undefined
// behavior, do not do this!
func (z *ZmqSubscriber) Subscribe(ctx context.Context, endpoint string, filter string) (<-chan struct{}, <-chan error, error) {
	zmqSocket, err := z.ctx.NewSocket(zmq4.SUB)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create ZMQ subscriber socket: %v", err)
	}

	err = zmqSocket.Connect(endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to ZMQ endpoint %s: %v", endpoint, err)
	}

	err = zmqSocket.SetSubscribe(filter)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to set ZMQ subscription filter %s: %v", filter, err)
	}

	logger := logging.GetLoggerFromContext(ctx).With("subscription", filter)

	msgChan := make(chan struct{}, 10)
	errChan := make(chan error)

	go func() {
		defer func() {
			logger.Info("[zmq] Closing subscriber socket...")
			if err := zmqSocket.Close(); err != nil {
				logger.Error("[zmq] Failed to close subscriber socket", "error", err)
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
				_, err := zmqSocket.RecvMessage(0)
				if err != nil {
					if zmq4.AsErrno(err) != zmq4.ETERM {
						logger.Error("[zmq] Failed to receive message", "error", err)

						select {
						case errChan <- fmt.Errorf("failed to receive message: %v", err):
						default:
							logger.Warn("[zmq] No receiver for error channel, dropping error...", "error", err)
						}
					}

					return
				}

				logger.Info(fmt.Sprintf("[zmq] Message received!"))
				select {
				case msgChan <- struct{}{}:
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

// Close closes the ZMQ socket and terminates the context. This should be called when the subscriber
// is no longer needed.
func (z *ZmqSubscriber) Close() error {
	// This will block until all sockets are closed, so we must make sure to handle `zmq4.ETERM` in
	// our sockets and make sure they are closed in response to it!
	return z.ctx.Term()
}
