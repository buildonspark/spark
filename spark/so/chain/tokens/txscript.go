package tokens

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/btcsuite/btcd/txscript"
)

// validatePushBytes parses a Bitcoin script push operation and validates its format.
// It advances the buffer past the push metadata. Returns nil if valid.
// Handles OP_PUSHDATA1 (0x4c), OP_PUSHDATA2 (0x4d), OP_PUSHDATA4 (0x4e), and direct pushes (0x01-0x4b).
func validatePushBytes(script *bytes.Buffer) error {
	totalLen := script.Len() + 1 // Account for OP_RETURN
	if totalLen <= 2 {
		return fmt.Errorf("script too short: no push operation")
	}

	pushOp, err := readByte(script)
	if err != nil {
		return err
	}

	var dataLength int
	switch {
	case pushOp >= 0x01 && pushOp <= 0x4b:
		dataLength = int(pushOp)
	case pushOp == txscript.OP_PUSHDATA1:
		length, err := readByte(script)
		if err != nil {
			return fmt.Errorf("script too short for OP_PUSHDATA1")
		}
		dataLength = int(length)
	case pushOp == txscript.OP_PUSHDATA2:
		lengthBytes := script.Next(2)
		if len(lengthBytes) != 2 {
			return fmt.Errorf("script too short for OP_PUSHDATA2")
		}
		dataLength = int(binary.LittleEndian.Uint16(lengthBytes))
	case pushOp == txscript.OP_PUSHDATA4:
		lengthBytes := script.Next(4)
		if len(lengthBytes) != 4 {
			return fmt.Errorf("script too short for OP_PUSHDATA4")
		}
		dataLength = int(binary.LittleEndian.Uint32(lengthBytes))
	default:
		return fmt.Errorf("unparseable pushBytes")
	}

	if script.Len() != dataLength {
		return fmt.Errorf("script length mismatch: expected %d bytes, got %d", dataLength, script.Len())
	}

	return nil
}

func readBytes(buf *bytes.Buffer, want int) ([]byte, error) {
	asBytes := buf.Next(want)
	if len(asBytes) != want {
		return nil, fmt.Errorf("insufficient data: expected %d byte(s), got %d", want, len(asBytes))
	}
	return asBytes, nil
}

func readByte(buf *bytes.Buffer) (byte, error) {
	asByte, err := buf.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("insufficient data: expected 1 byte, got 0")
	}
	return asByte, nil
}
