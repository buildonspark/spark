package utils

import (
	"testing"

	"github.com/go-playground/assert/v2"
)

func TestIndexToIdentifier(t *testing.T) {
	identifier := IndexToIdentifier(0)
	assert.Equal(t, identifier, "0000000000000000000000000000000000000000000000000000000000000001")
	assert.Equal(t, len(identifier), 64)

	identifier = IndexToIdentifier(1)
	assert.Equal(t, identifier, "0000000000000000000000000000000000000000000000000000000000000002")
	assert.Equal(t, len(identifier), 64)

	identifier = IndexToIdentifier(2)
	assert.Equal(t, identifier, "0000000000000000000000000000000000000000000000000000000000000003")
	assert.Equal(t, len(identifier), 64)
}
