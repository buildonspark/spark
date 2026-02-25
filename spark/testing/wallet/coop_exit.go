package wallet

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"

	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/frost"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/lightsparkdev/spark/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
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

// GetConnectorRefundSignaturesV2WithTransferPackage performs a single-call cooperative exit
// by building a TransferPackage (with user-signed refunds and key tweaks) and sending it
// directly in the CooperativeExitV2 call. No separate FinalizeTransferWithTransferPackage
// call is needed.
func GetConnectorRefundSignaturesV2WithTransferPackage(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	exitTxid []byte,
	connectorOutputs []*wire.OutPoint,
	receiverPubKey keys.Public,
	expiryTime time.Time,
	connectorTx []byte,
) (*pb.Transfer, error) {
	if len(leaves) != len(connectorOutputs) {
		return nil, fmt.Errorf("number of leaves and connector outputs must match")
	}

	connectorTxParsed, err := common.TxFromRawTxBytes(connectorTx)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connector tx: %w", err)
	}

	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}

	// Prepare key tweaks
	keyTweakInputMap, err := PrepareSendTransferKeyTweaks(config, transferID, receiverPubKey, leaves, map[string][]byte{})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare key tweaks: %w", err)
	}

	// Authenticate and create client
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with coordinator: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	// Get signing commitments: 3 per leaf (CPFP, Direct, DirectFromCpfp)
	const maxRefundTxsPerLeaf = 3
	nodes := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodes[i] = leaf.Leaf.Id
	}
	signingCommitments, err := sparkClient.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodes,
		Count:   maxRefundTxsPerLeaf,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get signing commitments: %w", err)
	}
	commitmentsByLeafID := extractCommitmentsByLeaf(leaves, signingCommitments.SigningCommitments)

	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()
	signerClient := pbfrost.NewFrostServiceClient(signerConn)

	// Build refund txs and user-sign them
	var cpfpLeaves []LeafKeyTweak
	var cpfpRefundTxs []*wire.MsgTx
	var cpfpConnectorPrevOutputs []*wire.TxOut
	var cpfpNodeTxs []*wire.MsgTx

	var directFromCpfpLeaves []LeafKeyTweak
	var directFromCpfpRefundTxs []*wire.MsgTx
	var directFromCpfpConnectorPrevOutputs []*wire.TxOut
	var directFromCpfpNodeTxs []*wire.MsgTx

	var directLeaves []LeafKeyTweak
	var directRefundTxs []*wire.MsgTx
	var directConnectorPrevOutputs []*wire.TxOut
	var directNodeTxs []*wire.MsgTx

	for i, leaf := range leaves {
		connectorOutput := connectorOutputs[i]

		if leaf.Leaf == nil || leaf.Leaf.RefundTx == nil {
			return nil, fmt.Errorf("leaf at index %d has nil Leaf or RefundTx", i)
		}

		currentRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}
		sequence, directSequence, err := bitcointransaction.NextSequence(currentRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		nodeOutPoint := &currentRefundTx.TxIn[0].PreviousOutPoint

		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		if len(nodeTx.TxOut) == 0 {
			return nil, fmt.Errorf("node tx has no outputs")
		}
		nodeAmountSats := nodeTx.TxOut[0].Value

		isZeroNode := bitcointransaction.GetTimelockFromSequence(nodeTx.TxIn[0].Sequence) == 0

		var directTx *wire.MsgTx
		var directOutPoint *wire.OutPoint
		var directAmountSats int64
		if len(leaf.Leaf.DirectTx) > 0 {
			directTx, err = common.TxFromRawTxBytes(leaf.Leaf.DirectTx)
			if err != nil {
				return nil, fmt.Errorf("failed to parse direct tx: %w", err)
			}
			if len(directTx.TxOut) == 0 {
				return nil, fmt.Errorf("direct tx has no outputs")
			}
			directOutPoint = &wire.OutPoint{Hash: directTx.TxHash(), Index: 0}
			directAmountSats = directTx.TxOut[0].Value
		}

		cpfpRefundTx, directFromCpfpRefundTx, directRefundTx, err := CreateAllRefundTxs(
			sequence, directSequence,
			nodeOutPoint, nodeAmountSats,
			directOutPoint, directAmountSats,
			receiverPubKey, true,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create refund txs: %w", err)
		}

		// Add connector input to CPFP and DirectFromCpfp
		cpfpRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
		directFromCpfpRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))

		connectorPrevOutput := connectorTxParsed.TxOut[connectorOutput.Index]

		cpfpLeaves = append(cpfpLeaves, leaf)
		cpfpRefundTxs = append(cpfpRefundTxs, cpfpRefundTx)
		cpfpConnectorPrevOutputs = append(cpfpConnectorPrevOutputs, connectorPrevOutput)
		cpfpNodeTxs = append(cpfpNodeTxs, nodeTx)

		directFromCpfpLeaves = append(directFromCpfpLeaves, leaf)
		directFromCpfpRefundTxs = append(directFromCpfpRefundTxs, directFromCpfpRefundTx)
		directFromCpfpConnectorPrevOutputs = append(directFromCpfpConnectorPrevOutputs, connectorPrevOutput)
		directFromCpfpNodeTxs = append(directFromCpfpNodeTxs, nodeTx)

		if !isZeroNode && directRefundTx != nil {
			directRefundTx.AddTxIn(wire.NewTxIn(connectorOutput, nil, nil))
			directLeaves = append(directLeaves, leaf)
			directRefundTxs = append(directRefundTxs, directRefundTx)
			directConnectorPrevOutputs = append(directConnectorPrevOutputs, connectorPrevOutput)
			directNodeTxs = append(directNodeTxs, directTx)
		}
	}

	// Sign CPFP refund txs (user side) with multi-input sighash
	cpfpCommitments := make([]*pb.RequestedSigningCommitments, len(cpfpLeaves))
	for i, leaf := range cpfpLeaves {
		cpfpCommitments[i] = commitmentsByLeafID[leaf.Leaf.Id][0]
	}
	cpfpLeafSigningJobs, err := signCoopExitUserRefunds(
		tmpCtx, signerClient, cpfpLeaves, cpfpRefundTxs, cpfpNodeTxs,
		cpfpConnectorPrevOutputs, cpfpCommitments,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign CPFP refund txs: %w", err)
	}

	// Sign DirectFromCpfp refund txs (user side) with multi-input sighash
	directFromCpfpCommitments := make([]*pb.RequestedSigningCommitments, len(directFromCpfpLeaves))
	for i, leaf := range directFromCpfpLeaves {
		directFromCpfpCommitments[i] = commitmentsByLeafID[leaf.Leaf.Id][2]
	}
	directFromCpfpLeafSigningJobs, err := signCoopExitUserRefunds(
		tmpCtx, signerClient, directFromCpfpLeaves, directFromCpfpRefundTxs,
		directFromCpfpNodeTxs, directFromCpfpConnectorPrevOutputs, directFromCpfpCommitments,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign DirectFromCpfp refund txs: %w", err)
	}

	// Sign Direct refund txs (user side) — these also have connector input for coop exit
	var directLeafSigningJobs []*pb.UserSignedTxSigningJob
	if len(directLeaves) > 0 {
		directCommitments := make([]*pb.RequestedSigningCommitments, len(directLeaves))
		for i, leaf := range directLeaves {
			directCommitments[i] = commitmentsByLeafID[leaf.Leaf.Id][1]
		}
		directLeafSigningJobs, err = signCoopExitUserRefundsForDirect(
			tmpCtx, signerClient, directLeaves, directRefundTxs, directNodeTxs,
			directConnectorPrevOutputs, directCommitments,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to sign Direct refund txs: %w", err)
		}
	}

	// Encrypt key tweaks per SO
	encryptedKeyTweaks := make(map[string][]byte)
	for identifier, keyTweaks := range keyTweakInputMap {
		protoToEncrypt := pb.SendLeafKeyTweaks{LeavesToSend: keyTweaks}
		protoToEncryptBinary, err := proto.Marshal(&protoToEncrypt)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal proto to encrypt: %w", err)
		}
		encryptionKeyBytes := config.SigningOperators[identifier].IdentityPublicKey
		encryptionKey, err := eciesgo.NewPublicKeyFromBytes(encryptionKeyBytes.Serialize())
		if err != nil {
			return nil, fmt.Errorf("failed to parse encryption key: %w", err)
		}
		encryptedProto, err := eciesgo.Encrypt(encryptionKey, protoToEncryptBinary)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt proto: %w", err)
		}
		encryptedKeyTweaks[identifier] = encryptedProto
	}

	transferPackage := &pb.TransferPackage{
		LeavesToSend:               cpfpLeafSigningJobs,
		DirectFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
		DirectLeavesToSend:         directLeafSigningJobs,
		KeyTweakPackage:            encryptedKeyTweaks,
	}

	transferPackageSigningPayload := common.GetTransferPackageSigningPayload(transferID, transferPackage)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), transferPackageSigningPayload)
	transferPackage.UserSignature = signature.Serialize()

	exitID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate exit id: %w", err)
	}

	response, err := sparkClient.CooperativeExitV2(tmpCtx, &pb.CooperativeExitRequest{
		Transfer: &pb.StartTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverPubKey.Serialize(),
			ExpiryTime:                timestamppb.New(expiryTime),
			TransferPackage:           transferPackage,
		},
		ExitId:      exitID.String(),
		ExitTxid:    exitTxid,
		ConnectorTx: connectorTx,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initiate cooperative exit: %w", err)
	}

	return response.Transfer, nil
}

// signCoopExitUserRefunds creates user-side FROST signing jobs for coop exit refund txs
// with multi-input sighash (leaf output + connector output).
func signCoopExitUserRefunds(
	ctx context.Context,
	signerClient pbfrost.FrostServiceClient,
	leaves []LeafKeyTweak,
	refundTxs []*wire.MsgTx,
	nodeTxs []*wire.MsgTx,
	connectorPrevOutputs []*wire.TxOut,
	signingCommitments []*pb.RequestedSigningCommitments,
) ([]*pb.UserSignedTxSigningJob, error) {
	var signingJobs []*pbfrost.FrostSigningJob
	rawRefundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*frost.SigningCommitment, len(leaves))

	for i, leaf := range leaves {
		refundTx := refundTxs[i]
		nodeTx := nodeTxs[i]

		var refundBuf bytes.Buffer
		if err := refundTx.Serialize(&refundBuf); err != nil {
			return nil, fmt.Errorf("failed to serialize refund tx: %w", err)
		}
		rawRefundTxs[i] = refundBuf.Bytes()

		// Compute multi-input sighash
		prevOutputs := map[wire.OutPoint]*wire.TxOut{
			refundTx.TxIn[0].PreviousOutPoint: nodeTx.TxOut[0],
			refundTx.TxIn[1].PreviousOutPoint: connectorPrevOutputs[i],
		}
		sighash, err := common.SigHashFromMultiPrevOutTx(refundTx, 0, prevOutputs)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate multi-input sighash: %w", err)
		}

		signingNonce := frost.GenerateSigningNonce()
		signingNonceProto, _ := signingNonce.MarshalProto()
		signingCommitment := signingNonce.SigningCommitment()
		userCommitmentProto, _ := signingCommitment.MarshalProto()
		userCommitments[i] = &signingCommitment

		userKeyPackage := CreateUserKeyPackage(leaf.SigningPrivKey)
		signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
			JobId:           leaf.Leaf.Id,
			Message:         sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    leaf.Leaf.VerifyingPublicKey,
			Nonce:           signingNonceProto,
			Commitments:     signingCommitments[i].SigningNonceCommitments,
			UserCommitments: userCommitmentProto,
		})
	}

	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}

	return prepareLeafSigningJobs(leaves, rawRefundTxs, signingResults.Results, userCommitments, signingCommitments)
}

// signCoopExitUserRefundsForDirect creates user-side FROST signing jobs for direct refund txs.
// Direct refund txs spend from DirectTx and also have a connector input for coop exit.
func signCoopExitUserRefundsForDirect(
	ctx context.Context,
	signerClient pbfrost.FrostServiceClient,
	leaves []LeafKeyTweak,
	refundTxs []*wire.MsgTx,
	directTxs []*wire.MsgTx,
	connectorPrevOutputs []*wire.TxOut,
	signingCommitments []*pb.RequestedSigningCommitments,
) ([]*pb.UserSignedTxSigningJob, error) {
	var signingJobs []*pbfrost.FrostSigningJob
	rawRefundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*frost.SigningCommitment, len(leaves))

	for i, leaf := range leaves {
		refundTx := refundTxs[i]
		directTx := directTxs[i]

		var refundBuf bytes.Buffer
		if err := refundTx.Serialize(&refundBuf); err != nil {
			return nil, fmt.Errorf("failed to serialize direct refund tx: %w", err)
		}
		rawRefundTxs[i] = refundBuf.Bytes()

		// Direct refund txs spend from DirectTx and include a connector input for coop exit
		prevOutputs := map[wire.OutPoint]*wire.TxOut{
			refundTx.TxIn[0].PreviousOutPoint: directTx.TxOut[0],
			refundTx.TxIn[1].PreviousOutPoint: connectorPrevOutputs[i],
		}
		sighash, err := common.SigHashFromMultiPrevOutTx(refundTx, 0, prevOutputs)
		if err != nil {
			return nil, fmt.Errorf("failed to calculate sighash for direct refund: %w", err)
		}

		signingNonce := frost.GenerateSigningNonce()
		signingNonceProto, _ := signingNonce.MarshalProto()
		signingCommitment := signingNonce.SigningCommitment()
		userCommitmentProto, _ := signingCommitment.MarshalProto()
		userCommitments[i] = &signingCommitment

		userKeyPackage := CreateUserKeyPackage(leaf.SigningPrivKey)
		signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
			JobId:           leaf.Leaf.Id,
			Message:         sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    leaf.Leaf.VerifyingPublicKey,
			Nonce:           signingNonceProto,
			Commitments:     signingCommitments[i].SigningNonceCommitments,
			UserCommitments: userCommitmentProto,
		})
	}

	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign frost: %w", err)
	}

	return prepareLeafSigningJobs(leaves, rawRefundTxs, signingResults.Results, userCommitments, signingCommitments)
}
