package protoconverter

import (
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
)

// ConvertTokenTransactionStatusToTokenPb converts from st.TokenTransactionStatus to tokenpb.TokenTransactionStatus
func ConvertTokenTransactionStatusToTokenPb(status st.TokenTransactionStatus) tokenpb.TokenTransactionStatus {
	switch status {
	case st.TokenTransactionStatusStarted:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED
	case st.TokenTransactionStatusStartedCancelled:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_STARTED_CANCELLED
	case st.TokenTransactionStatusSigned:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED
	case st.TokenTransactionStatusSignedCancelled:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_SIGNED_CANCELLED
	case st.TokenTransactionStatusRevealed:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_REVEALED
	case st.TokenTransactionStatusFinalized:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_FINALIZED
	default:
		return tokenpb.TokenTransactionStatus_TOKEN_TRANSACTION_UNKNOWN
	}
}
