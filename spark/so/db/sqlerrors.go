package db

import (
	"strings"

	"entgo.io/ent/dialect/sql/sqlgraph"
)

// IsLockNotAvailableError returns true if the error is a PostgreSQL
// "lock_not_available" error (SQLSTATE 55P03), which occurs when using
// NoWait lock acquisition on a row that is already locked
func IsLockNotAvailableError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLSTATE 55P03")
}

// IsRetriableSQLStateError returns true for transient database errors that might
// succeed on retry (connection issues, timeouts, resource exhaustion).
// Returns false for constraint violations which will always fail on retry
func IsRetriableSQLStateError(err error) bool {
	if err == nil {
		return false
	}
	if sqlgraph.IsConstraintError(err) {
		return false
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "SQLSTATE") {
		return false
	}
	// Class 23 errors (integrity constraint violations) are not retriable
	if strings.Contains(errStr, "SQLSTATE 23") {
		return false
	}
	return true
}
