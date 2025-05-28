package common

type MockAction struct {
	InterruptTransfer bool
}

func NewMockAction() *MockAction {
	return &MockAction{
		InterruptTransfer: false,
	}
}
