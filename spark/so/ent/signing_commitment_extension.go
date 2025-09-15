package ent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/logging"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/lightsparkdev/spark/so/ent/signingcommitment"
)

// ReserveSigningCommitments gets unused signing commitments from the database.
// The caller must provide a new database transaction to use.
// The caller must commit or rollback the transaction after using the returned commitments.
// This call is supposed to be used only in the SigningCommitmentInterceptor before any grpc flows.
func ReserveSigningCommitments(ctx context.Context, dbTx *Tx, count uint32, operatorIndex uint) ([]*SigningCommitment, error) {
	logger := logging.GetLoggerFromContext(ctx)
	logger.Sugar().Infof("Getting %d unused signing commitments for operator %d", count, operatorIndex)
	commitments, err := dbTx.SigningCommitment.Query().Where(
		signingcommitment.And(
			signingcommitment.StatusEQ(st.SigningCommitmentStatusAvailable),
			signingcommitment.OperatorIndex(operatorIndex),
		),
	).Limit(int(count)).ForUpdate().All(ctx)
	if err != nil {
		return nil, err
	}

	if len(commitments) < int(count) {
		return nil, fmt.Errorf("not enough unused signing commitments: %d", len(commitments))
	}

	commitmentIDs := make([]uuid.UUID, len(commitments))
	for i, commitment := range commitments {
		commitmentIDs[i] = commitment.ID
	}

	if err := dbTx.SigningCommitment.Update().Where(signingcommitment.IDIn(commitmentIDs...)).SetStatus(st.SigningCommitmentStatusUsed).Exec(ctx); err != nil {
		return nil, err
	}

	return commitments, nil
}
