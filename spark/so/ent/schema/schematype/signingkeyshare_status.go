package schematype

// SigningKeyshareStatus is the status of a signing keyshare.
type SigningKeyshareStatus string

const (
	// KeyshareStatusPending is waiting confirmation for being successfully written from other SOs.
	KeyshareStatusPending SigningKeyshareStatus = "PENDING"
	// KeyshareStatusAbandoned is the status if a pending keyshare was never confirmed written and
	// available on other SOs after some period of time.
	KeyshareStatusAbandoned SigningKeyshareStatus = "ABANDONED"
	// KeyshareStatusAvailable is the status of a signing keyshare that is available.
	KeyshareStatusAvailable SigningKeyshareStatus = "AVAILABLE"
	// KeyshareStatusInUse is the status of a signing keyshare that is in use.
	KeyshareStatusInUse SigningKeyshareStatus = "IN_USE"
)

// Values returns the values of the signing keyshare status.
func (SigningKeyshareStatus) Values() []string {
	return []string{
		string(KeyshareStatusPending),
		string(KeyshareStatusAbandoned),
		string(KeyshareStatusAvailable),
		string(KeyshareStatusInUse),
	}
}
