package wallet

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
)

// GetConnectorRefundSignatures asks the coordinator to sign refund
// transactions for leaves, spending connector outputs.
// Deprecated: use GetConnectorRefundSignaturesV2 instead.
func GetConnectorRefundSignatures(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, error) {
	transfer, signaturesMap, err := signCoopExitRefunds(
		ctx, config, leaves, exitTxid, connectorOutputs, receiverPubKey, expiryTime,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %w", err)
	}

	transfer, err = SendTransferTweakKey(ctx, config, transfer, leaves, signaturesMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to send transfer: %w", err)
	}

	return transfer, signaturesMap, nil
}

// GetConnectorRefundSignaturesV2 asks the coordinator to sign refund
// transactions for leaves, spending connector outputs.
// This version takes a client parameter and uses DeliverTransferPackage.
func GetConnectorRefundSignaturesV2(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, error) {
	transfer, signaturesMap, err := signCoopExitRefunds(
		ctx, config, leaves, exitTxid, connectorOutputs, receiverPubKey, expiryTime,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %w", err)
	}

	transfer, err = DeliverTransferPackage(ctx, config, transfer, leaves, signaturesMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to deliver transfer package: %w", err)
	}

	return transfer, signaturesMap, nil
}

func createConnectorRefundTransactionSigningJob(
	leafID string,
	signingPubKey keys.Public,
	nonce *objects.SigningNonce,
	refundTx *wire.MsgTx,
) (*pb.LeafRefundTxSigningJob, error) {
	var refundBuf bytes.Buffer
	err := refundTx.Serialize(&refundBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
	}
	rawTx := refundBuf.Bytes()
	// TODO(alec): we don't handle errors for this elsewhere, should we here?
	refundNonceCommitmentProto, _ := nonce.SigningCommitment().MarshalProto()

	return &pb.LeafRefundTxSigningJob{
		LeafId: leafID,
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rawTx,
			SigningNonceCommitment: refundNonceCommitmentProto,
		},
	}, nil
}

func signCoopExitRefunds(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey keys.Public,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, error) {
	if len(leaves) != len(connectorOutputs) {
		return nil, nil, fmt.Errorf("number of leaves and connector outputs must match")
	}
	var signingJobs []*pb.LeafRefundTxSigningJob
	leafDataMap := make(map[string]*LeafRefundSigningData)
	for i, leaf := range leaves {
		connectorOutput := connectorOutputs[i]

		if leaf.Leaf == nil {
			return nil, nil, fmt.Errorf("leaf at index %d has nil Leaf field", i)
		}
		if leaf.Leaf.RefundTx == nil {
			return nil, nil, fmt.Errorf("leaf at index %d has nil RefundTx field", i)
		}

		currentRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}
		sequence, err := spark.NextSequence(currentRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		refundTx, err := createConnectorRefundTransaction(
			sequence, &currentRefundTx.TxIn[0].PreviousOutPoint, connectorOutput, int64(leaf.Leaf.Value), receiverPubKey,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create refund transaction: %w", err)
		}
		nonce, _ := objects.RandomSigningNonce()
		signingJob, err := createConnectorRefundTransactionSigningJob(
			leaf.Leaf.Id, leaf.SigningPrivKey.Public(), nonce, refundTx,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create signing job: %w", err)
		}
		signingJobs = append(signingJobs, signingJob)

		tx, _ := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)

		leafDataMap[leaf.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey: leaf.SigningPrivKey,
			RefundTx:       refundTx,
			Nonce:          nonce,
			Tx:             tx,
			Vout:           int(leaf.Leaf.Vout),
		}
	}

	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to authenticate with coordinator: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	exitID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate exit id: %w", err)
	}
	response, err := sparkClient.CooperativeExitV2(tmpCtx, &pb.CooperativeExitRequest{
		Transfer: &pb.StartTransferRequest{
			TransferId:                transferID.String(),
			LeavesToSend:              signingJobs,
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(expiryTime),
		},
		ExitId:   exitID.String(),
		ExitTxid: exitTxid,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initiate cooperative exit: %w", err)
	}
	signatures, err := signRefunds(config, leafDataMap, response.SigningResults, keys.Public{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %w", err)
	}

	signaturesMap := make(map[string][]byte)
	for _, signature := range signatures {
		signaturesMap[signature.NodeId] = signature.RefundTxSignature
	}

	return response.Transfer, signaturesMap, nil
}
