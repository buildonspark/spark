package schematype

import pb "github.com/lightsparkdev/spark/proto/spark"

type UtxoSwapRequestType string

const (
	UtxoSwapRequestTypeFixedAmount UtxoSwapRequestType = "FIXED_AMOUNT"
	UtxoSwapRequestTypeMaxFee      UtxoSwapRequestType = "MAX_FEE"
	UtxoSwapRequestTypeRefund      UtxoSwapRequestType = "REFUND"
	UtxoSwapRequestTypeInstant     UtxoSwapRequestType = "INSTANT"
)

func (UtxoSwapRequestType) Values() []string {
	return []string{
		string(UtxoSwapRequestTypeFixedAmount),
		string(UtxoSwapRequestTypeMaxFee),
		string(UtxoSwapRequestTypeRefund),
		string(UtxoSwapRequestTypeInstant),
	}
}

func UtxoSwapFromProtoRequestType(requestType pb.UtxoSwapRequestType) UtxoSwapRequestType {
	switch requestType {
	case pb.UtxoSwapRequestType_Fixed:
		return UtxoSwapRequestTypeFixedAmount
	case pb.UtxoSwapRequestType_MaxFee:
		return UtxoSwapRequestTypeMaxFee
	case pb.UtxoSwapRequestType_Refund:
		return UtxoSwapRequestTypeRefund
	case pb.UtxoSwapRequestType_Instant:
		return UtxoSwapRequestTypeInstant
	default:
		return UtxoSwapRequestTypeFixedAmount
	}
}
