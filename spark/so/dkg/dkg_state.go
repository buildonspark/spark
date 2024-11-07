package dkg

import (
	"bytes"
	"crypto/ecdsa"
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
	Type                   DkgStateType
	Round1Package          [][]byte
	ReceivedRound1Packages []map[string][]byte
	ReceivedRound2Packages []map[string][]byte
}

type DkgStates struct {
	mu     sync.RWMutex
	states map[string]*DkgState
}

func NewDkgStates() *DkgStates {
	return &DkgStates{
		states: make(map[string]*DkgState),
	}
}

func (s *DkgStates) GetState(requestId string) (*DkgState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.states[requestId]
	if !ok {
		return nil, fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	return state, nil
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

func (s *DkgStates) ReceivedRound1Packages(requestId string, selfIdentifier string, round1Packages []map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestId]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	if state.Type != Round1 {
		return fmt.Errorf("dkg state is not in round 1 state for request id: %s", requestId)
	}

	if len(round1Packages) != len(state.Round1Package) {
		return fmt.Errorf("received round 1 packages has wrong number of keys for request id: %s", requestId)
	}

	for i, p := range round1Packages {
		selfPackage, ok := p[selfIdentifier]
		if !ok {
			return fmt.Errorf("self package is not included in round 1 packages for request id: %s", requestId)
		}

		if !bytes.Equal(state.Round1Package[i], selfPackage) {
			return fmt.Errorf("round 1 package %d is not the same as the self package for request id: %s", i, requestId)
		}
	}

	state.Type = Round1Signature
	state.ReceivedRound1Packages = round1Packages
	s.states[requestId] = state
	return nil
}

func (s *DkgStates) ReceivedRound1Signature(requestId string, selfIdentifier string, round1Signatures map[string][]byte, publicKeyMap map[string]ecdsa.PublicKey) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestId]
	if !ok {
		return nil, fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	if state.Type != Round1Signature {
		return nil, fmt.Errorf("dkg state is not in round 1 signature state for request id: %s", requestId)
	}

	valid, validationFailures := ValidateRound1Signature(state.ReceivedRound1Packages, round1Signatures, publicKeyMap)
	if !valid {
		// Abort the DKG process
		s.states[requestId] = &DkgState{
			Type: Failed,
		}

		return validationFailures, nil
	}

	state.Type = Round2
	s.states[requestId] = state
	return nil, nil
}
