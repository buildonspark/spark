package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"

	"github.com/lightsparkdev/spark-go/common"
	"github.com/lightsparkdev/spark-go/proto/spark"
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
	TreeNode       *spark.TreeNode
}

// GetConnectorRefundSignatures asks the coordinator to sign refund
// transactions for leaves, spending connector outputs.
func GetConnectorRefundSignatures(
	ctx context.Context,
	config *Config,
	signingPrivKey *secp256k1.PrivateKey,
	leaves []*Leaf,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey *secp256k1.PublicKey,
) ([]*pb.NodeSignatures, error) {
	exitID, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("failed to create exit id: %v", err)
	}
	if len(leaves) != len(connectorOutputs) {
		return nil, fmt.Errorf("number of leaves and connector outputs must match")
	}
	signingJobs := make([]*pb.LeafRefundTxSigningJob, 0)
	leafDataMap := make(map[string]*leafRefundSigningData)
	for i, leaf := range leaves {
		connectorOutput := connectorOutputs[i]
		refundTx, err := createConnectorRefundTransaction(
			leaf.RefundTimeLock, leaf.OutPoint, connectorOutput, leaf.AmountSats, receiverPubKey,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create refund transaction: %v", err)
		}
		nonce, _ := objects.RandomSigningNonce()
		signingJob, err := createConnectorRefundTransactionSigningJob(
			leaf.LeafID, leaf.SigningPubKey.SerializeCompressed(), nonce, refundTx,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create signing job: %v", err)
		}
		signingJobs = append(signingJobs, signingJob)

		tx, _ := common.TxFromRawTxBytes(leaf.TreeNode.NodeTx)

		leafDataMap[leaf.LeafID] = &leafRefundSigningData{
			SigningPrivKey: signingPrivKey,
			Tx:             tx,
			RefundTx:       refundTx,
			Nonce:          nonce,
			Vout:           0,
		}
	}

	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to create grpc connection: %v", err)
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.CooperativeExit(ctx, &pb.CooperativeExitRequest{
		ExitId:                 exitID.String(),
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
		ExitTxid:               exitTxid,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initiate cooperative exit: %v", err)
	}
	return signRefunds(config, leafDataMap, response.SigningResults)
}

func createConnectorRefundTransaction(
	sequence uint32,
	nodeOutPoint *wire.OutPoint,
	connectorOutput *wire.OutPoint,
	amountSats int64,
	receiverPubKey *secp256k1.PublicKey,
) (*wire.MsgTx, error) {
	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: *nodeOutPoint,
		SignatureScript:  nil, // TODO? SO should know what to put here
		Witness:          nil,
		Sequence:         sequence,
	})
	refundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
	receiverScript, err := common.P2TRScriptFromPubKey(receiverPubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create receiver script: %v", err)
	}
	refundTx.AddTxOut(wire.NewTxOut(amountSats, receiverScript))
	return refundTx, nil
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
