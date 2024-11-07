package dkg

import (
	"fmt"
	"sync"
)

type DkgStateType int

const (
	// Initial state when DKG process starts
	Initial DkgStateType = iota
	// Round1 state after receiving round 1 packages
	Round1
	// Round1Signature state after receiving round 1 signatures
	Round1Signature
	// Round2 state after receiving round 2 packages
	Round2
	// Completed state when DKG process is finished
	Completed
	// Failed state if DKG process fails
	Failed
)

type DkgState struct {
	Type DkgStateType
	Round1Package [][]byte
	ReceivedRound1Packages map[string][][]byte
	Round2Package []map[string][]byte
	ReceivedRound2Packages map[string][]map[string][]byte
}

type DkgStates struct {
	mu sync.RWMutex
	states map[string]*DkgState
}

func NewDkgStates() *DkgStates {
	return &DkgStates{
		states: make(map[string]*DkgState),
	}
}

func (s *DkgStates) InitiateDkg(requestId string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.states[requestId]; ok {
		return fmt.Errorf("dkg state already exists for request id: %s", requestId)
	}

	s.states[requestId] = &DkgState{
		Type: Initial,
	}

	return nil
}

func (s *DkgStates) ProvideRound1Package(requestId string, round1Package [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestId]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	if state.Type != Initial {
		return fmt.Errorf("dkg state is not in initial state for request id: %s", requestId)
	}

	state.Round1Package = round1Package
	state.Type = Round1
	s.states[requestId] = state
	return nil
}
