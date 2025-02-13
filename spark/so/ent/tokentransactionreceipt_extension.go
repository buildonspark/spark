package ent

import (
	"context"
	"encoding/hex"
	"log"

	"github.com/google/uuid"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/ent/schema"
	"github.com/lightsparkdev/spark-go/so/utils"
)

func GetReceiptMapFromList(receipts []*TokenTransactionReceipt) (map[string]*TokenTransactionReceipt, error) {
	receiptMap := make(map[string]*TokenTransactionReceipt)
	for _, r := range receipts {
		if len(r.FinalizedTokenTransactionHash) > 0 {
			key := hex.EncodeToString(r.FinalizedTokenTransactionHash)
			receiptMap[key] = r
		}
	}
	return receiptMap, nil
}

func SaveTokenTransactionReceiptAndLeafEnts(ctx context.Context, tokenTransaction *pb.TokenTransaction, tokenTransactionSignatures *pb.TokenTransactionSignatures, leafRevocationKeyshareIDs []string) (*TokenTransactionReceipt, error) {
	partialTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, true)
	if err != nil {
		log.Printf("Failed to hash partial token transaction: %v", err)
		return nil, err
	}
	finalTokenTransactionHash, err := utils.HashTokenTransaction(tokenTransaction, false)
	if err != nil {
		log.Printf("Failed to hash final token transaction: %v", err)
		return nil, err
	}
	db := GetDbFromContext(ctx)
	tokenTransactionReceipt, err := db.TokenTransactionReceipt.Create().
		SetPartialTokenTransactionHash(partialTokenTransactionHash).
		SetFinalizedTokenTransactionHash(finalTokenTransactionHash).
		Save(ctx)
	if err != nil {
		log.Printf("Failed to create token transaction receipt: %v", err)
		return nil, err
	}

	if tokenTransaction.GetIssueInput() != nil {
		_, err = db.TokenIssuance.Create().
			SetIssuerPublicKey(tokenTransaction.GetIssueInput().GetIssuerPublicKey()).
			SetIssuerSignature(tokenTransactionSignatures.GetOwnerSignatures()[0]).
			Save(ctx)
		if err != nil {
			log.Printf("Failed to create token issuance ent: %v", err)
			return nil, err
		}
	}

	outputLeaves := make([]*TokenLeafCreate, 0, len(tokenTransaction.OutputLeaves))
	for leafIndex, outputLeaf := range tokenTransaction.OutputLeaves {
		revocationUUID, err := uuid.Parse(leafRevocationKeyshareIDs[leafIndex])
		if err != nil {
			return nil, err
		}
		leafUUID, err := uuid.Parse(outputLeaf.Id)
		if err != nil {
			return nil, err
		}
		outputLeaves = append(
			outputLeaves,
			db.TokenLeaf.
				Create().
				// TODO: Consider whether the coordinator instead of the wallet should define this ID.
				SetID(leafUUID).
				SetStatus(schema.TokenLeafStatusCreatedStarted).
				SetOwnerPublicKey(outputLeaf.OwnerPublicKey).
				SetWithdrawalBondSats(outputLeaf.WithdrawalBondSats).
				SetWithdrawalLocktime(outputLeaf.WithdrawalLocktime).
				SetWithdrawalRevocationPublicKey(outputLeaf.RevocationPublicKey).
				SetTokenPublicKey(outputLeaf.TokenPublicKey).
				SetTokenAmount(outputLeaf.TokenAmount).
				SetLeafCreatedTransactionOutputVout(uint32(leafIndex)).
				SetRevocationKeyshareID(revocationUUID).
				SetLeafCreatedTokenTransactionReceiptID(tokenTransactionReceipt.ID),
		)
	}
	_, err = db.TokenLeaf.CreateBulk(outputLeaves...).Save(ctx)
	if err != nil {
		log.Printf("Failed to create token leaves: %v", err)
		return nil, err
	}
	return tokenTransactionReceipt, nil
}
