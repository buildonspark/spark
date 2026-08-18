package schematype

// TokenAllowanceStatus is the lifecycle status of a token spending allowance.
type TokenAllowanceStatus string

const (
	// TokenAllowanceStatusActive is the default status; the allowance may still authorize spends.
	TokenAllowanceStatusActive TokenAllowanceStatus = "ACTIVE"
	// TokenAllowanceStatusRevoked is the status after the owner tombstones the allowance.
	TokenAllowanceStatusRevoked TokenAllowanceStatus = "REVOKED"
	// TokenAllowanceStatusExhausted is the status once the total limit has been fully spent.
	TokenAllowanceStatusExhausted TokenAllowanceStatus = "EXHAUSTED"
)

// Values returns the values of the token allowance status.
func (TokenAllowanceStatus) Values() []string {
	return []string{
		string(TokenAllowanceStatusActive),
		string(TokenAllowanceStatusRevoked),
		string(TokenAllowanceStatusExhausted),
	}
}
