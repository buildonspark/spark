package ent

import (
	"context"
	"fmt"
)

// SaveWithdrawalTransaction creates a new L1WithdrawalTransaction entity.
func SaveWithdrawalTransaction(ctx context.Context, dbClient *Client, tx *L1WithdrawalTransaction, seEntity *EntityDkgKey) (*L1WithdrawalTransaction, error) {
	return dbClient.L1WithdrawalTransaction.Create().
		SetConfirmationTxid(tx.ConfirmationTxid).
		SetConfirmationBlockHash(tx.ConfirmationBlockHash).
		SetConfirmationHeight(tx.ConfirmationHeight).
		SetDetectedAt(tx.DetectedAt).
		SetOwnerSignature(tx.OwnerSignature).
		SetSeEntity(seEntity).
		Save(ctx)
}

// SaveOutputWithdrawals creates L1TokenOutputWithdrawal entities for each output.
func SaveOutputWithdrawals(ctx context.Context, dbClient *Client, bitcoinVouts []uint16, tokenOutputs []*TokenOutput, withdrawalTx *L1WithdrawalTransaction) ([]*L1TokenOutputWithdrawal, error) {
	results := make([]*L1TokenOutputWithdrawal, 0, len(bitcoinVouts))
	for i, vout := range bitcoinVouts {
		saved, err := dbClient.L1TokenOutputWithdrawal.Create().
			SetBitcoinVout(vout).
			SetTokenOutput(tokenOutputs[i]).
			SetL1WithdrawalTransaction(withdrawalTx).
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to save output withdrawal: %w", err)
		}
		results = append(results, saved)
	}

	return results, nil
}
