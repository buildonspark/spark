package tokens

import (
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
)

// BuildSignedCommitProgress builds a CommitProgress from the token transaction's peer signatures.
// This determines which operators have signed the transaction based on:
// - Peer signatures stored in the transaction edges
// - The operator's own signature (if operatorSignature is present)
//
// Returns an error if the peer signatures edge was not loaded.
func BuildSignedCommitProgress(tt *ent.TokenTransaction, config *so.Config) (*tokenpb.CommitProgress, error) {
	peerSigs := tt.Edges.PeerSignatures
	if peerSigs == nil {
		return nil, sparkerrors.InternalDatabaseMissingEdge(fmt.Errorf("peer signatures edge not loaded"))
	}

	seen := map[keys.Public]struct{}{}
	for _, ps := range peerSigs {
		seen[ps.OperatorIdentityPublicKey] = struct{}{}
	}

	// Add self if we have signed (operator signature present).
	if len(tt.OperatorSignature) > 0 {
		seen[config.IdentityPublicKey()] = struct{}{}
	}

	var committed, uncommitted [][]byte
	for _, operator := range config.SigningOperatorMap {
		operatorPublicKey := operator.IdentityPublicKey
		if _, ok := seen[operatorPublicKey]; ok {
			committed = append(committed, operatorPublicKey.Serialize())
		} else {
			uncommitted = append(uncommitted, operatorPublicKey.Serialize())
		}
	}

	return &tokenpb.CommitProgress{
		CommittedOperatorPublicKeys:   committed,
		UncommittedOperatorPublicKeys: uncommitted,
	}, nil
}

// BuildRevealCommitProgress determines which operators have provided their secret shares
// for ALL spent outputs in a transfer transaction.
//
// An operator is considered "committed" only if they have revealed shares for every output.
// Returns an error if the spent outputs edge was not loaded.
func BuildRevealCommitProgress(tt *ent.TokenTransaction, config *so.Config) (*tokenpb.CommitProgress, error) {
	outputsToCheck := tt.Edges.SpentOutput
	if len(outputsToCheck) == 0 {
		return nil, sparkerrors.InternalDatabaseMissingEdge(
			fmt.Errorf("no spent outputs found for transfer token transaction %x", tt.FinalizedTokenTransactionHash),
		)
	}

	// Get all known operator public keys
	allOperatorPubKeys := make([]keys.Public, 0, len(config.SigningOperatorMap))
	for _, operator := range config.SigningOperatorMap {
		allOperatorPubKeys = append(allOperatorPubKeys, operator.IdentityPublicKey)
	}

	// Determine which operators have provided their secret shares for each output
	operatorSharesPerOutput := make(map[int]map[keys.Public]struct{}) // output_index -> operator_key -> has_share
	coordinatorKey := config.IdentityPublicKey()

	for i, output := range outputsToCheck {
		operatorSharesPerOutput[i] = make(map[keys.Public]struct{})
		// If coordinator has revocation keyshare, mark as revealed
		if output.Edges.RevocationKeyshare != nil {
			operatorSharesPerOutput[i][coordinatorKey] = struct{}{}
		}
		// Add all operators that have provided partial shares
		if output.Edges.TokenPartialRevocationSecretShares != nil {
			for _, partialShare := range output.Edges.TokenPartialRevocationSecretShares {
				operatorSharesPerOutput[i][partialShare.OperatorIdentityPublicKey] = struct{}{}
			}
		}
	}

	// An operator is committed only if they have shares for ALL outputs
	var committed, uncommitted [][]byte
	for _, operatorKey := range allOperatorPubKeys {
		hasAllShares := true
		for i := range outputsToCheck {
			if _, exists := operatorSharesPerOutput[i][operatorKey]; !exists {
				hasAllShares = false
				break
			}
		}
		if hasAllShares {
			committed = append(committed, operatorKey.Serialize())
		} else {
			uncommitted = append(uncommitted, operatorKey.Serialize())
		}
	}

	return &tokenpb.CommitProgress{
		CommittedOperatorPublicKeys:   committed,
		UncommittedOperatorPublicKeys: uncommitted,
	}, nil
}
