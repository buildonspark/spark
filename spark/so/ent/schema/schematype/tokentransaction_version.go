package schematype

type TokenTransactionVersion int

const (
	// TokenTransactionVersionV0 is the initial version of the token transaction.
	TokenTransactionVersionV0 TokenTransactionVersion = iota
	// TokenTransactionVersionV1 is the version of the token transaction that
	// 1) improves handling of empty
	// 2) adds expiry time and client created timestamp
	TokenTransactionVersionV1
	// TokenTransactionVersionV2 is the version of the token transaction that
	// 1) Adds invoice attachments
	TokenTransactionVersionV2
	// TokenTransactionVersionV3 is the version of the token transaction that
	// implements token transaction autohashing and sorting of list fields
	TokenTransactionVersionV3
)

// ValidValues returns the valid version values
func (TokenTransactionVersion) ValidValues() []TokenTransactionVersion {
	return []TokenTransactionVersion{
		TokenTransactionVersionV0,
		TokenTransactionVersionV1,
		TokenTransactionVersionV2,
		TokenTransactionVersionV3,
	}
}

// IsValid checks if the version is valid
func (v TokenTransactionVersion) IsValid() bool {
	return v == TokenTransactionVersionV0 || v == TokenTransactionVersionV1 || v == TokenTransactionVersionV2 || v == TokenTransactionVersionV3
}
