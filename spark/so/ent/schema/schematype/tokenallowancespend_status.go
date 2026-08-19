package schematype

// TokenAllowanceSpendStatus tracks whether a metered allowance spend still holds budget.
type TokenAllowanceSpendStatus string

const (
	// TokenAllowanceSpendStatusReserved is the default status while the metered amount is held against the allowance.
	TokenAllowanceSpendStatusReserved TokenAllowanceSpendStatus = "RESERVED"
	// TokenAllowanceSpendStatusReleased is the status after the reservation is returned to the allowance budget.
	TokenAllowanceSpendStatusReleased TokenAllowanceSpendStatus = "RELEASED"
)

// Values returns the values of the token allowance spend status.
func (TokenAllowanceSpendStatus) Values() []string {
	return []string{
		string(TokenAllowanceSpendStatusReserved),
		string(TokenAllowanceSpendStatusReleased),
	}
}
