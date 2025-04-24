package events

import (
	"encoding/hex"
	"fmt"
	"sync"

	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

var (
	defaultRouter *EventRouter
	routerOnce    sync.Once
)

func GetDefaultRouter() *EventRouter {
	routerOnce.Do(func() {
		defaultRouter = NewEventRouter()
	})
	return defaultRouter
}

type EventRouter struct {
	streams sync.Map
	mutexes sync.Map
}

func NewEventRouter() *EventRouter {
	return &EventRouter{
		streams: sync.Map{},
		mutexes: sync.Map{},
	}
}

func (s *EventRouter) RegisterStream(identityPublicKey []byte, stream pb.SparkService_SubscribeToEventsServer) error {
	identityPublicKeyHex := hex.EncodeToString(identityPublicKey)

	mutex, _ := s.mutexes.LoadOrStore(identityPublicKeyHex, &sync.Mutex{})
	mutex.(*sync.Mutex).Lock()
	defer mutex.(*sync.Mutex).Unlock()

	s.streams.Store(identityPublicKeyHex, stream)
	go func() {
		<-stream.Context().Done()
		if mutex, ok := s.mutexes.Load(identityPublicKeyHex); ok {
			mutex.(*sync.Mutex).Lock()
			defer mutex.(*sync.Mutex).Unlock()

			if current, ok := s.streams.Load(identityPublicKeyHex); ok {
				if current.(pb.SparkService_SubscribeToEventsServer) == stream {
					s.streams.Delete(identityPublicKeyHex)
					s.mutexes.Delete(identityPublicKeyHex)
				}
			}
		}
	}()

	return nil
}

func (s *EventRouter) NotifyUser(identityPublicKey []byte, message *pb.SubscribeToEventsResponse) error {
	identityPublicKeyHex := hex.EncodeToString(identityPublicKey)

	mutex, _ := s.mutexes.Load(identityPublicKeyHex)
	if mutex == nil {
		return nil
	}
	mutex.(*sync.Mutex).Lock()
	defer mutex.(*sync.Mutex).Unlock()

	if currentStream, ok := s.streams.Load(identityPublicKeyHex); ok {
		if err := currentStream.(pb.SparkService_SubscribeToEventsServer).Send(message); err != nil {
			peer, ok := peer.FromContext(currentStream.(pb.SparkService_SubscribeToEventsServer).Context())

			s.streams.Delete(identityPublicKeyHex)
			s.mutexes.Delete(identityPublicKeyHex)

			if !isStreamClosedError(err) {
				network := "unknown"
				address := "unknown"
				if ok {
					network = peer.Addr.Network()
					address = peer.Addr.String()
				}

				return fmt.Errorf("error sending message to stream for (network: %s, address: %s): %v", network, address, err)
			}
		}
	}

	return nil
}

func SubscribeToEvents(identityPublicKey []byte, st pb.SparkService_SubscribeToEventsServer) error {
	streamRouter := GetDefaultRouter()
	if err := streamRouter.RegisterStream(identityPublicKey, st); err != nil {
		return err
	}

	<-st.Context().Done()
	return nil
}

func isStreamClosedError(err error) bool {
	if err == nil {
		return false
	}

	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Canceled, codes.Unavailable, codes.DeadlineExceeded:
			return true
		default:
			return false
		}
	}

	return false
}
