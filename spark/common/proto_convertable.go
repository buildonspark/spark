package common

import "google.golang.org/protobuf/proto"

// ProtoConvertable is an interface that allows a struct to be converted to a protobuf message.
type ProtoConvertable[T proto.Message] interface {
	// MarshalProto converts the struct to a protobuf message.
	MarshalProto() (T, error)
}
