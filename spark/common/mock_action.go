package common

import "fmt"

type MockAction struct {
	// Cooperative-exit failure toggles
	InterruptCoopExit bool   // master switch
	TargetOperatorID  string // fail only this SO (identifier, e.g. “0001”)
}

func NewMockAction() *MockAction {
	return &MockAction{
		InterruptCoopExit: false,
		TargetOperatorID:  fmt.Sprintf("%064x", 1+2), // operator 0002
	}
}
