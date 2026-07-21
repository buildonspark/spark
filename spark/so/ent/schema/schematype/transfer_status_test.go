package schematype

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTransferStatusIsTerminal(t *testing.T) {
	terminal := map[TransferStatus]bool{
		TransferStatusCompleted: true,
		TransferStatusExpired:   true,
		TransferStatusReturned:  true,
	}
	for _, v := range (TransferStatus("")).Values() {
		s := TransferStatus(v)
		assert.Equal(t, terminal[s], s.IsTerminal(), "IsTerminal(%s)", s)
	}
}

func TestNonTerminalTransferStatuses_ExcludesExactlyTheTerminalSet(t *testing.T) {
	nonTerminal := NonTerminalTransferStatuses()
	assert.Len(t, nonTerminal, len((TransferStatus("")).Values())-3)
	for _, s := range nonTerminal {
		assert.False(t, s.IsTerminal())
	}
	assert.Contains(t, nonTerminal, TransferStatusApplyingSenderKeyTweak)
	assert.NotContains(t, nonTerminal, TransferStatusCompleted)
	assert.NotContains(t, nonTerminal, TransferStatusExpired)
	assert.NotContains(t, nonTerminal, TransferStatusReturned)
}
