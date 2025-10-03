package schematype

import (
	"fmt"

	pb "github.com/lightsparkdev/spark/proto/spark"
)

// PreimageRequestStatus is the status of the preimage request
type PreimageRequestStatus string

const (
	// PreimageRequestStatusWaitingForPreimage is the status of the preimage request when it is waiting for preimage
	PreimageRequestStatusWaitingForPreimage PreimageRequestStatus = "WAITING_FOR_PREIMAGE"
	// PreimageRequestStatusPreimageShared is the status of the preimage request when it is preimage shared
	PreimageRequestStatusPreimageShared PreimageRequestStatus = "PREIMAGE_SHARED"
	// PreimageRequestStatusReturned is the status of the preimage request when it is returned
	PreimageRequestStatusReturned PreimageRequestStatus = "RETURNED"
)

// MarshalProto converts a PreimageRequestStatus to a spark protobuf PreimageRequestStatus.
func (p PreimageRequestStatus) MarshalProto() (pb.PreimageRequestStatus, error) {
	switch p {
	case PreimageRequestStatusWaitingForPreimage:
		return pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, nil
	case PreimageRequestStatusPreimageShared:
		return pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_PREIMAGE_SHARED, nil
	case PreimageRequestStatusReturned:
		return pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_RETURNED, nil
	default:
		return pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE, fmt.Errorf("unknown preimage request status: %s", p)
	}
}

// UnmarshalProto converts a spark protobuf PreimageRequestStatus to a PreimageRequestStatus.
func (p *PreimageRequestStatus) UnmarshalProto(proto pb.PreimageRequestStatus) error {
	switch proto {
	case pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_WAITING_FOR_PREIMAGE:
		*p = PreimageRequestStatusWaitingForPreimage
	case pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_PREIMAGE_SHARED:
		*p = PreimageRequestStatusPreimageShared
	case pb.PreimageRequestStatus_PREIMAGE_REQUEST_STATUS_RETURNED:
		*p = PreimageRequestStatusReturned
	default:
		return fmt.Errorf("unknown preimage request status: %d", proto)
	}
	return nil
}

// Values returns the values of the preimage request status
func (PreimageRequestStatus) Values() []string {
	return []string{
		string(PreimageRequestStatusWaitingForPreimage),
		string(PreimageRequestStatusPreimageShared),
		string(PreimageRequestStatusReturned),
	}
}
