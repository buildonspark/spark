package wallet

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
)

// Leaf to represent input for GetConnectorRefundSignatures.
// This should probably be combined with some other input struct
// we do for transfers.
type Leaf struct {
	LeafID         string
	OutPoint       *wire.OutPoint
	SigningPubKey  *secp256k1.PublicKey
	RefundTimeLock uint32
	AmountSats     int64
	TreeNode       *pb.TreeNode
}

// GetConnectorRefundSignatures asks the coordinator to sign refund
// transactions for leaves, spending connector outputs.
func GetConnectorRefundSignatures(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey *secp256k1.PublicKey,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, error) {
	transfer, signaturesMap, err := signCoopExitRefunds(
		ctx, config, leaves, exitTxid, connectorOutputs, receiverPubKey, expiryTime,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %v", err)
	}

	transfer, err = SendTransferTweakKey(ctx, config, transfer, leaves, signaturesMap)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to send transfer: %v", err)
	}

	return transfer, signaturesMap, nil
}

func createConnectorRefundTransactionSigningJob(
	leafID string,
	signingPubkey []byte,
	nonce *objects.SigningNonce,
	refundTx *wire.MsgTx,
) (*pb.LeafRefundTxSigningJob, error) {
	var refundBuf bytes.Buffer
	err := refundTx.Serialize(&refundBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %v", err)
	}
	rawTx := refundBuf.Bytes()
	// TODO(alec): we don't handle errors for this elsewhere, should we here?
	refundNonceCommitmentProto, _ := nonce.SigningCommitment().MarshalProto()

	return &pb.LeafRefundTxSigningJob{
		LeafId: leafID,
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       signingPubkey,
			RawTx:                  rawTx,
			SigningNonceCommitment: refundNonceCommitmentProto,
		},
	}, nil
}

func signCoopExitRefunds(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey *secp256k1.PublicKey,
	expiryTime time.Time,
) (*pb.Transfer, map[string][]byte, error) {
	if len(leaves) != len(connectorOutputs) {
		return nil, nil, fmt.Errorf("number of leaves and connector outputs must match")
	}
	signingJobs := make([]*pb.LeafRefundTxSigningJob, 0)
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
			return nil, nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}
		sequence, err := spark.NextSequence(currentRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence: %v", err)
		}
		refundTx, err := createConnectorRefundTransaction(
			sequence, &currentRefundTx.TxIn[0].PreviousOutPoint, connectorOutput, int64(leaf.Leaf.Value), receiverPubKey,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create refund transaction: %v", err)
		}
		nonce, _ := objects.RandomSigningNonce()
		signingPrivKey := secp256k1.PrivKeyFromBytes(leaf.SigningPrivKey)
		signingPubKey := signingPrivKey.PubKey()
		signingJob, err := createConnectorRefundTransactionSigningJob(
			leaf.Leaf.Id, signingPubKey.SerializeCompressed(), nonce, refundTx,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create signing job: %v", err)
		}
		signingJobs = append(signingJobs, signingJob)

		tx, _ := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)

		leafDataMap[leaf.Leaf.Id] = &LeafRefundSigningData{
			SigningPrivKey: signingPrivKey,
			RefundTx:       refundTx,
			Nonce:          nonce,
			Tx:             tx,
			Vout:           int(leaf.Leaf.Vout),
		}
	}

	sparkConn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to authenticate with coordinator: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}
	exitID, err := uuid.NewV7()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate exit id: %v", err)
	}
	response, err := sparkClient.CooperativeExit(tmpCtx, &pb.CooperativeExitRequest{
		Transfer: &pb.StartSendTransferRequest{
			TransferId:                transferID.String(),
			LeavesToSend:              signingJobs,
			OwnerIdentityPublicKey:    config.IdentityPublicKey(),
			ReceiverIdentityPublicKey: receiverPubKey.SerializeCompressed(),
			ExpiryTime:                timestamppb.New(expiryTime),
		},
		ExitId:   exitID.String(),
		ExitTxid: exitTxid,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initiate cooperative exit: %v", err)
	}
	signatures, err := signRefunds(config, leafDataMap, response.SigningResults, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %v", err)
	}

	signaturesMap := make(map[string][]byte)
	for _, signature := range signatures {
		signaturesMap[signature.NodeId] = signature.RefundTxSignature
	}

	return response.Transfer, signaturesMap, nil
}
