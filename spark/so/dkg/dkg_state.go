package dkg

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"google.golang.org/grpc"

	// Import the sqlite driver
	_ "github.com/mattn/go-sqlite3"
)

// StateType is the name for each different state of the DKG state machine.
type StateType int

const (
	// Initial state when DKG process starts
	Initial StateType = iota
	// Round1 state after receiving round 1 packages
	Round1
	// Round1Signature state after receiving round 1 signatures
	Round1Signature
	// Round2 state after receiving round 2 packages
	Round2
)

// State is the state machine for the DKG protocol.
type State struct {
	// Type is the current state of the DKG state machine.
	Type StateType
	// MaxSigners is the maximum number of signers.
	MaxSigners uint64
	// MinSigners is the minimum number of signers.
	MinSigners uint64
	// CoordinatorIndex is the index of the coordinator.
	CoordinatorIndex uint64
	// Round1Package is the round 1 package.
	Round1Package [][]byte
	// ReceivedRound1Packages is the round 1 packages received from other operators.
	ReceivedRound1Packages []map[string][]byte
	// ReceivedRound2Packages is the round 2 packages received from other operators.
	ReceivedRound2Packages []map[string][]byte
	// CreatedAt is the time when the DKG state was created.
	CreatedAt time.Time
}

// States is a collection of DKG states with a mutex for concurrent access.
type States struct {
	mu     sync.RWMutex
	states map[string]*State
}

// NewStates creates a new DKG states collection.
func NewStates() *States {
	return &States{
		states: make(map[string]*State),
	}
}

// GetState returns the DKG state for the given request id.
// If the state does not exist, it returns an error.
func (s *States) GetState(requestID string) (*State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.states[requestID]
	if !ok {
		return nil, fmt.Errorf("dkg state does not exist for request id: %s", requestID)
	}

	return state, nil
}

// InitiateDkg initializes a new DKG state for the given request id.
// If the state already exists, it returns an error.
func (s *States) InitiateDkg(requestID string, maxSigners uint64, minSigners uint64, coordinatorIndex uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.states[requestID]; ok {
		return fmt.Errorf("dkg state already exists for request id: %s", requestID)
	}

	if s.states == nil {
		s.states = make(map[string]*State)
	}

	s.states[requestID] = &State{
		Type:             Initial,
		MaxSigners:       maxSigners,
		MinSigners:       minSigners,
		CoordinatorIndex: coordinatorIndex,
		CreatedAt:        time.Now(),
	}

	return nil
}

// ProvideRound1Package provides the round 1 package for the given request id.
// If the state is not in the initial state, it returns an error.
func (s *States) ProvideRound1Package(requestID string, round1Package [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestID]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestID)
	}

	if state.Type != Initial {
		return fmt.Errorf("dkg state is not in initial state for request id: %s", requestID)
	}

	state.Round1Package = round1Package
	state.Type = Round1
	s.states[requestID] = state
	return nil
}

// ReceivedRound1Packages receives the round 1 packages from other operators.
// If the state is not in the round 1 state, it returns an error.
func (s *States) ReceivedRound1Packages(requestID string, selfIdentifier string, round1Packages []map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestID]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestID)
	}

	if state.Type != Round1 {
		return fmt.Errorf("dkg state is not in round 1 state for request id: %s", requestID)
	}

	if len(round1Packages) != len(state.Round1Package) {
		return fmt.Errorf("received round 1 packages has wrong number of keys for request id: %s", requestID)
	}

	for i, p := range round1Packages {
		selfPackage, ok := p[selfIdentifier]
		if !ok {
			return fmt.Errorf("self package is not included in round 1 packages for request id: %s", requestID)
		}

		if !bytes.Equal(state.Round1Package[i], selfPackage) {
			return fmt.Errorf("round 1 package %d is not the same as the self package for request id: %s", i, requestID)
		}
	}

	state.Type = Round1Signature
	state.ReceivedRound1Packages = round1Packages
	s.states[requestID] = state
	return nil
}

// ReceivedRound1Signature receives the round 1 signatures from other operators.
// If the state is not in the round 1 signature state, it returns an error.
func (s *States) ReceivedRound1Signature(requestID string, _ string, round1Signatures map[string][]byte, operatorMap map[string]*so.SigningOperator) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestID]
	if !ok {
		return nil, fmt.Errorf("dkg state does not exist for request id: %s", requestID)
	}

	if state.Type != Round1Signature {
		return nil, fmt.Errorf("dkg state is not in round 1 signature state for request id: %s", requestID)
	}

	valid, validationFailures := validateRound1Signature(state.ReceivedRound1Packages, round1Signatures, operatorMap)
	if !valid {
		// Abort the DKG process
		delete(s.states, requestID)

		return validationFailures, nil
	}

	state.Type = Round2
	s.states[requestID] = state

	return nil, nil
}

// ReceivedRound2Packages receives the round 2 packages from other operators.
// If the state is not in the round 1 signature or round 2 state, it returns an error.
func (s *States) ReceivedRound2Packages(requestID string, identifier string, round2Packages [][]byte, _ []byte, _ *grpc.ClientConn, _ *so.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestID]
	if !ok {
		return fmt.Errorf("dkg state does not exist for request id: %s", requestID)
	}

	if state.Type != Round2 && state.Type != Round1Signature {
		return fmt.Errorf("dkg state is not in round 2 or round 1 signature state for request id: %s", requestID)
	}

	if len(state.ReceivedRound2Packages) == 0 {
		state.ReceivedRound2Packages = make([]map[string][]byte, len(round2Packages))
		for i := range state.ReceivedRound2Packages {
			state.ReceivedRound2Packages[i] = make(map[string][]byte)
		}
	}

	for i, p := range round2Packages {
		state.ReceivedRound2Packages[i][identifier] = p
	}

	s.states[requestID] = state
	return nil
}

// ProceedToRound3 checks if we can proceed to round 3 for the given request id.
// We can proceed to round 3 if we have received the round 2 packages from all operators as well as we got our own round 2 package.
// If we can, it will perform the round 3.
func (s *States) ProceedToRound3(ctx context.Context, requestID string, frostConnection *grpc.ClientConn, config *so.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.states[requestID]
	if !ok {
		// This call might be called twice per state. So this should not count as an error.
		return nil
	}

	if len(state.ReceivedRound2Packages) == 0 {
		return nil
	}
	if int64(len(state.ReceivedRound2Packages[0])) == int64(state.MaxSigners-1) && state.Type == Round2 {
		delete(s.states, requestID)

		err := state.Round3(ctx, requestID, frostConnection, config)
		if err != nil {
			return err
		}
	}
	return nil
}

// Round3 performs the round 3 of the DKG protocol.
// This will generate the keyshares and store them in the database.
func (s *State) Round3(ctx context.Context, requestID string, frostConnection *grpc.ClientConn, _ *so.Config) error {
	round1PackagesMaps := make([]*pbcommon.PackageMap, len(s.ReceivedRound1Packages))
	for i, p := range s.ReceivedRound1Packages {
		round1PackagesMaps[i] = &pbcommon.PackageMap{
			Packages: p,
		}
	}

	round2PackagesMaps := make([]*pbcommon.PackageMap, len(s.ReceivedRound2Packages))
	for i, p := range s.ReceivedRound2Packages {
		round2PackagesMaps[i] = &pbcommon.PackageMap{
			Packages: p,
		}
	}

	frostClient := pbfrost.NewFrostServiceClient(frostConnection)
	response, err := frostClient.DkgRound3(ctx, &pbfrost.DkgRound3Request{
		RequestId:          requestID,
		Round1PackagesMaps: round1PackagesMaps,
		Round2PackagesMaps: round2PackagesMaps,
	})
	if err != nil {
		return err
	}

	if len(response.KeyPackages) == 0 {
		return nil
	}

	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	signingKeyshares := make([]*ent.SigningKeyshareCreate, len(response.KeyPackages))
	for i, key := range response.KeyPackages {
		batchID, err := uuid.Parse(requestID)
		if err != nil {
			return err
		}
		keyID := deriveKeyIndex(batchID, uint16(i))
		pubKeyMap, err := keys.ParsePublicKeyMap(key.PublicShares)
		if err != nil {
			return err
		}
		pubKey, err := keys.ParsePublicKey(key.PublicKey)
		if err != nil {
			return err
		}
		signingKeyshares[i] = db.SigningKeyshare.Create().
			SetID(keyID).
			SetStatus(st.KeyshareStatusAvailable).
			SetMinSigners(int32(s.MinSigners)).
			SetSecretShare(key.SecretShare).
			SetPublicShares(pubKeyMap).
			SetPublicKey(pubKey).
			SetCoordinatorIndex(s.CoordinatorIndex)
	}

	err = db.SigningKeyshare.CreateBulk(signingKeyshares...).Exec(ctx)
	if err != nil {
		return err
	}

	return nil
}

// RemoveState removes the DKG state for the given request id.
func (s *States) RemoveState(requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.states, requestID)
}
