package wallet

import (
	"bytes"
	"context"
	"fmt"
	"time"

	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/frost"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

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
	connectorTx []byte,
) (*pb.Transfer, map[string][]byte, error) {
	transfer, signaturesMap, err := signCoopExitRefunds(
		ctx, config, leaves, exitTxid, connectorOutputs, receiverPubKey, expiryTime, connectorTx,
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

func createCoopExitRefundTransactionSigningJob(
	leafID string,
	signingPubKey keys.Public,
	refundNonce frost.SigningNonce,
	refundTx *wire.MsgTx,
	directRefundNonce *frost.SigningNonce,
	directRefundTx *wire.MsgTx,
	directFromCpfpNonce frost.SigningNonce,
	directFromCpfpRefundTx *wire.MsgTx,
) (*pb.LeafRefundTxSigningJob, error) {
	var refundBuf bytes.Buffer
	if err := refundTx.Serialize(&refundBuf); err != nil {
		return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
	}
	rawRefundTx := refundBuf.Bytes()
	refundNonceCommitmentProto, _ := refundNonce.SigningCommitment().MarshalProto()

	var directFromCpfpRefundBuf bytes.Buffer
	if err := directFromCpfpRefundTx.Serialize(&directFromCpfpRefundBuf); err != nil {
		return nil, fmt.Errorf("failed to serialize direct from cpfp refund tx: %w", err)
	}
	rawDirectFromCpfpRefundTx := directFromCpfpRefundBuf.Bytes()
	directFromCpfpRefundNonceCommitmentProto, _ := directFromCpfpNonce.SigningCommitment().MarshalProto()

	job := &pb.LeafRefundTxSigningJob{
		LeafId: leafID,
		RefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rawRefundTx,
			SigningNonceCommitment: refundNonceCommitmentProto,
		},
		DirectFromCpfpRefundTxSigningJob: &pb.SigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rawDirectFromCpfpRefundTx,
			SigningNonceCommitment: directFromCpfpRefundNonceCommitmentProto,
		},
	}

	// Only add DirectRefundTxSigningJob for non-zero nodes
	if directRefundTx != nil && directRefundNonce != nil {
		var directRefundBuf bytes.Buffer
		if err := directRefundTx.Serialize(&directRefundBuf); err != nil {
			return nil, fmt.Errorf("failed to serialize direct refund tx: %w", err)
		}
		rawDirectRefundTx := directRefundBuf.Bytes()
		directRefundNonceCommitmentProto, _ := directRefundNonce.SigningCommitment().MarshalProto()

		job.DirectRefundTxSigningJob = &pb.SigningJob{
			SigningPublicKey:       signingPubKey.Serialize(),
			RawTx:                  rawDirectRefundTx,
			SigningNonceCommitment: directRefundNonceCommitmentProto,
		}
	}

	return job, nil
}

func signCoopExitRefunds(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey keys.Public,
	expiryTime time.Time,
	connectorTx []byte,
) (*pb.Transfer, map[string][]byte, error) {
	if len(leaves) != len(connectorOutputs) {
		return nil, nil, fmt.Errorf("number of leaves and connector outputs must match")
	}

	connectorTxParsed, err := common.TxFromRawTxBytes(connectorTx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse connector tx: %w", err)
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
		sequence, directSequence, err := bitcointransaction.NextSequence(currentRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		nodeOutPoint := &currentRefundTx.TxIn[0].PreviousOutPoint

		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		if len(nodeTx.TxOut) == 0 {
			return nil, nil, fmt.Errorf("node tx has no outputs")
		}
		nodeAmountSats := nodeTx.TxOut[0].Value

		isZeroNode := bitcointransaction.GetTimelockFromSequence(nodeTx.TxIn[0].Sequence) == 0

		var directTx *wire.MsgTx
		var directOutPoint *wire.OutPoint
		var directAmountSats int64
		if len(leaf.Leaf.DirectTx) > 0 {
			var err error
			directTx, err = common.TxFromRawTxBytes(leaf.Leaf.DirectTx)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to parse direct tx: %w", err)
			}
			if len(directTx.TxOut) == 0 {
				return nil, nil, fmt.Errorf("direct tx has no outputs")
			}
			directOutPoint = &wire.OutPoint{Hash: directTx.TxHash(), Index: 0}
			directAmountSats = directTx.TxOut[0].Value
		}

		cpfpRefundTx, directFromCpfpRefundTx, directRefundTx, err := CreateAllRefundTxs(
			sequence,
			directSequence,
			nodeOutPoint,
			nodeAmountSats,
			directOutPoint,
			directAmountSats,
			receiverPubKey,
			true,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create refund txs: %w", err)
		}

		cpfpRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
		directFromCpfpRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))

		refundNonce := frost.GenerateSigningNonce()
		directFromCpfpNonce := frost.GenerateSigningNonce()

		var directRefundNoncePtr *frost.SigningNonce
		var directRefundTxForJob *wire.MsgTx
		if !isZeroNode && directRefundTx != nil {
			directRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
			directRefundNonce := frost.GenerateSigningNonce()
			directRefundNoncePtr = &directRefundNonce
			directRefundTxForJob = directRefundTx
		}

		signingJob, err := createCoopExitRefundTransactionSigningJob(
			leaf.Leaf.Id,
			leaf.SigningPrivKey.Public(),
			refundNonce,
			cpfpRefundTx,
			directRefundNoncePtr,
			directRefundTxForJob,
			directFromCpfpNonce,
			directFromCpfpRefundTx,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create signing job: %w", err)
		}
		signingJobs = append(signingJobs, signingJob)

		connectorPrevOutput := connectorTxParsed.TxOut[connectorOutput.Index]
		leafData := &LeafRefundSigningData{
			SigningPrivKey:            leaf.SigningPrivKey,
			RefundTx:                  cpfpRefundTx,
			Nonce:                     &refundNonce,
			DirectTx:                  directTx,
			DirectFromCpfpRefundTx:    directFromCpfpRefundTx,
			DirectFromCpfpRefundNonce: &directFromCpfpNonce,
			Tx:                        nodeTx,
			Vout:                      int(leaf.Leaf.Vout),
			ConnectorPrevOutput:       connectorPrevOutput,
		}
		if !isZeroNode && directRefundTx != nil {
			leafData.DirectRefundTx = directRefundTx
			leafData.DirectRefundNonce = directRefundNoncePtr
		}
		leafDataMap[leaf.Leaf.Id] = leafData
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
		ExitId:      exitID.String(),
		ExitTxid:    exitTxid,
		ConnectorTx: connectorTx,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initiate cooperative exit: %w", err)
	}
	signatures, err := SignRefunds(config, leafDataMap, response.SigningResults, keys.Public{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sign refund transactions: %w", err)
	}

	signaturesMap := make(map[string][]byte)
	for _, signature := range signatures {
		signaturesMap[signature.NodeId] = signature.RefundTxSignature
	}

	return response.Transfer, signaturesMap, nil
}
