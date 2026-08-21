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
	// TokenAllowanceStatusExpired is the status once the owner-signed expiry has passed and an
	// operator has retired the grant. Retirement is bookkeeping, not enforcement: a spend past
	// the expiry is refused whatever the stored status says. Moving the row off ACTIVE releases
	// the owner's quota slot and the (owner, spender, token) uniqueness slot so a replacement
	// grant can be created.
	TokenAllowanceStatusExpired TokenAllowanceStatus = "EXPIRED"
)

// Values returns the values of the token allowance status.
func (TokenAllowanceStatus) Values() []string {
	return []string{
		string(TokenAllowanceStatusActive),
		string(TokenAllowanceStatusRevoked),
		string(TokenAllowanceStatusExhausted),
		string(TokenAllowanceStatusExpired),
	}
}
