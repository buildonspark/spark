package dkg

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	frost "github.com/lightsparkdev/spark-go/frost"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
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
)

type DkgState struct {
	Type                   DkgStateType
	MaxSigners             uint64
	Round1Package          [][]byte
	ReceivedRound1Packages []map[string][]byte
	ReceivedRound2Packages []map[string][]byte
	CreatedAt              time.Time
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

func (s *DkgStates) InitiateDkg(requestId string, maxSigners uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.states[requestId]; ok {
		return fmt.Errorf("dkg state already exists for request id: %s", requestId)
	}

	if s.states == nil {
		s.states = make(map[string]*DkgState)
	}

	s.states[requestId] = &DkgState{
		Type:       Initial,
		MaxSigners: maxSigners,
		CreatedAt:  time.Now(),
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

func (s *DkgStates) ReceivedRound1Signature(requestId string, selfIdentifier string, round1Signatures map[string][]byte, operatorMap map[string]*so.SigningOperator) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestId]
	if !ok {
		return nil, fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	if state.Type != Round1Signature {
		return nil, fmt.Errorf("dkg state is not in round 1 signature state for request id: %s", requestId)
	}

	valid, validationFailures := ValidateRound1Signature(state.ReceivedRound1Packages, round1Signatures, operatorMap)
	if !valid {
		// Abort the DKG process
		delete(s.states, requestId)

		return validationFailures, nil
	}

	state.Type = Round2
	s.states[requestId] = state
	return nil, nil
}

func (s *DkgStates) ReceivedRound2Packages(requestId string, identifier string, round2Packages [][]byte, round2Signature []byte, frostClient *frost.FrostClient) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestId]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestId)
	}

	if state.Type != Round2 && state.Type != Round1Signature {
		return fmt.Errorf("dkg state is not in round 2 or round 1 signature state for request id: %s", requestId)
	}

	if len(state.ReceivedRound2Packages) == 0 {
		log.Printf("Making new received round 2 packages")
		state.ReceivedRound2Packages = make([]map[string][]byte, len(round2Packages))
		for i := range state.ReceivedRound2Packages {
			state.ReceivedRound2Packages[i] = make(map[string][]byte)
		}
	}

	for i, p := range round2Packages {
		state.ReceivedRound2Packages[i][identifier] = p
	}

	log.Printf("Received round 2 packages: %v", len(state.ReceivedRound2Packages[0]))
	if int64(len(state.ReceivedRound2Packages[0])) == int64(state.MaxSigners-1) {
		delete(s.states, requestId)

		err := state.Round3(requestId, frostClient)
		if err != nil {
			return err
		}
	} else {
		s.states[requestId] = state
	}

	return nil
}

func (s *DkgState) Round3(requestId string, frostClient *frost.FrostClient) error {
	log.Printf("Round 3")
	round1PackagesMaps := make([]*pb.PackageMap, len(s.ReceivedRound1Packages))
	for i, p := range s.ReceivedRound1Packages {
		round1PackagesMaps[i] = &pb.PackageMap{
			Packages: p,
		}
	}

	round2PackagesMaps := make([]*pb.PackageMap, len(s.ReceivedRound2Packages))
	for i, p := range s.ReceivedRound2Packages {
		round2PackagesMaps[i] = &pb.PackageMap{
			Packages: p,
		}
	}

	response, err := frostClient.Client.DkgRound3(context.Background(), &pb.DkgRound3Request{
		RequestId:          requestId,
		Round1PackagesMaps: round1PackagesMaps,
		Round2PackagesMaps: round2PackagesMaps,
	})
	if err != nil {
		log.Printf("Error in round 3: %v", err)
		return err
	}

	for _, key := range response.KeyPackages {
		log.Printf("Public key: %x", key.PublicKey)
	}

	// TODO: store the response key in the db.

	return nil
}

func (s *DkgStates) RemoveState(requestId string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.states[requestId]; exists {
		delete(s.states, requestId)
	}
}
