package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	eciesgo "github.com/ecies/go/v2"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"

	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SwapNodesForPreimage swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimage(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
	amountSats uint64,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	nodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodeIDs[i] = leaf.Leaf.Id
	}

	// For RECEIVE, we need 3 commitments per leaf (CPFP, direct-from-cpfp, direct)
	// For SEND, we only need 1 commitment per leaf (CPFP)
	commitmentCount := 1
	if isInboundPayment {
		commitmentCount = 3
	}
	signingCommitments, err := client.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
		Count:   uint32(commitmentCount),
	})
	if err != nil {
		return nil, err
	}

	// SSP signs partial refund tx to receiver
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()

	signerClient := pbfrost.NewFrostServiceClient(signerConn)

	// SSP calls SO to get the preimage
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	expireTime := time.Now().Add(2 * time.Minute)

	bolt11String := ""
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			amountSats = uint64(bolt11.MSatoshi / 1000)
		}
	}

	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	var userSignedTransfer *pb.StartUserSignedTransferRequest

	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE

		// For RECEIVE, create P2TR refund txs with complete exit paths
		cpfpSigningCommitments := signingCommitments.SigningCommitments[:len(leaves)]
		directFromCpfpSigningCommitments := signingCommitments.SigningCommitments[len(leaves) : len(leaves)*2]
		directSigningCommitments := signingCommitments.SigningCommitments[len(leaves)*2 : len(leaves)*3]

		// 1. CPFP refund txs
		cpfpSigningJobs, cpfpRefundTxs, cpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefund(
			leaves, cpfpSigningCommitments, receiverIdentityPubKey, keys.Public{})
		if err != nil {
			return nil, fmt.Errorf("failed to prepare CPFP signing jobs: %w", err)
		}

		cpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: cpfpSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sign CPFP refund txs: %w", err)
		}

		cpfpLeafSigningJobs, err := prepareLeafSigningJobs(
			leaves, cpfpRefundTxs, cpfpSigningResults.Results, cpfpUserCommitments, cpfpSigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare CPFP leaf signing jobs: %w", err)
		}

		// 2. Direct-from-CPFP refund txs (spends from NodeTx with fee deduction)
		directFromCpfpSigningJobs, directFromCpfpRefundTxs, directFromCpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundDirect(
			leaves, directFromCpfpSigningCommitments, receiverIdentityPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare direct-from-cpfp signing jobs: %w", err)
		}

		directFromCpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: directFromCpfpSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sign direct-from-cpfp refund txs: %w", err)
		}

		directFromCpfpLeafSigningJobs, err := prepareLeafSigningJobs(
			leaves, directFromCpfpRefundTxs, directFromCpfpSigningResults.Results, directFromCpfpUserCommitments, directFromCpfpSigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare direct-from-cpfp leaf signing jobs: %w", err)
		}

		// 3. Direct refund txs (spends from DirectTx) - only for leaves that have DirectTx
		var directLeafSigningJobs []*pb.UserSignedTxSigningJob
		leavesWithDirect := filterLeavesWithDirectTx(leaves)
		if len(leavesWithDirect) > 0 {
			directSigningJobsFiltered, directRefundTxs, directUserCommitments, err := prepareFrostSigningJobsForDirectRefund(
				leavesWithDirect, directSigningCommitments[:len(leavesWithDirect)], receiverIdentityPubKey)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare direct signing jobs: %w", err)
			}

			directSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
				SigningJobs: directSigningJobsFiltered,
				Role:        pbfrost.SigningRole_USER,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to sign direct refund txs: %w", err)
			}

			directLeafSigningJobs, err = prepareLeafSigningJobs(
				leavesWithDirect, directRefundTxs, directSigningResults.Results, directUserCommitments, directSigningCommitments[:len(leavesWithDirect)])
			if err != nil {
				return nil, fmt.Errorf("failed to prepare direct leaf signing jobs: %w", err)
			}
		}

		userSignedTransfer = &pb.StartUserSignedTransferRequest{
			TransferId:                 transferID.String(),
			OwnerIdentityPublicKey:     config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey:  receiverIdentityPubKey.Serialize(),
			LeavesToSend:               cpfpLeafSigningJobs,
			DirectFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
			DirectLeavesToSend:         directLeafSigningJobs,
			ExpiryTime:                 timestamppb.New(expireTime),
		}
	} else {
		// For SEND, only need CPFP refund txs
		signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(
			leaves, signingCommitments.SigningCommitments, receiverIdentityPubKey, keys.Public{})
		if err != nil {
			return nil, err
		}

		signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: signingJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, err
		}

		leafSigningJobs, err := prepareLeafSigningJobs(
			leaves, refundTxs, signingResults.Results, userCommitments, signingCommitments.SigningCommitments)
		if err != nil {
			return nil, err
		}

		userSignedTransfer = &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(expireTime),
		}
	}

	response, err := client.InitiatePreimageSwapV2(tmpCtx, &pb.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash,
		Reason:      reason,
		InvoiceAmount: &pb.InvoiceAmount{
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: bolt11String,
			},
			ValueSats: amountSats,
		},
		Transfer:                  userSignedTransfer,
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		FeeSats:                   feeSats,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func QueryHTLC(
	ctx context.Context,
	config *TestWalletConfig,
	limit int64,
	offset int64,
	paymentHashes [][]byte,
	status *pb.PreimageRequestStatus,
	transferIds []string,
	matchRole *pb.PreimageRequestRole,
) (*pb.QueryHtlcResponse, error) {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)

	req := &pb.QueryHtlcRequest{
		IdentityPublicKey: config.IdentityPublicKey().Serialize(),
		Limit:             limit,
		Offset:            offset,
	}

	if matchRole != nil {
		req.MatchRole = *matchRole
	}

	if len(paymentHashes) > 0 {
		req.PaymentHashes = paymentHashes
	}

	if len(transferIds) > 0 {
		req.TransferIds = transferIds
	}

	if status != nil {
		req.Status = status
	}

	response, err := client.QueryHtlc(tmpCtx, req)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// SwapNodesForPreimage swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimageWithHTLC(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
	amountSats uint64,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	nodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodeIDs[i] = leaf.Leaf.Id
	}
	signingCommitments, err := client.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
		Count:   4, // 1 for original refund, 3 for htlc/p2tr refunds.
	})
	if err != nil {
		return nil, err
	}

	// SSP signs partial refund tx to receiver
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()

	signerClient := pbfrost.NewFrostServiceClient(signerConn)

	// SSP calls SO to get the preimage
	transferID, err := uuid.NewV7()
	expireTime := time.Now().Add(2 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	bolt11String := ""
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			amountSats = uint64(bolt11.MSatoshi / 1000)
		}
	}

	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	var transfer *pb.StartTransferRequest
	var userSignedTransfer *pb.StartUserSignedTransferRequest

	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE

		// For RECEIVE, create P2TR refund txs with complete exit paths
		cpfpSigningCommitments := signingCommitments.SigningCommitments[:len(leaves)]
		directFromCpfpSigningCommitments := signingCommitments.SigningCommitments[len(leaves) : len(leaves)*2]
		directSigningCommitments := signingCommitments.SigningCommitments[len(leaves)*2 : len(leaves)*3]

		// 1. CPFP refund txs
		cpfpSigningJobs, cpfpRefundTxs, cpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefund(
			leaves, cpfpSigningCommitments, receiverIdentityPubKey, keys.Public{})
		if err != nil {
			return nil, fmt.Errorf("failed to prepare CPFP signing jobs: %w", err)
		}

		cpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: cpfpSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sign CPFP refund txs: %w", err)
		}

		cpfpLeafSigningJobs, err := prepareLeafSigningJobs(
			leaves, cpfpRefundTxs, cpfpSigningResults.Results, cpfpUserCommitments, cpfpSigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare CPFP leaf signing jobs: %w", err)
		}

		// 2. Direct-from-CPFP refund txs (spends from NodeTx with fee deduction)
		directFromCpfpSigningJobs, directFromCpfpRefundTxs, directFromCpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundDirect(
			leaves, directFromCpfpSigningCommitments, receiverIdentityPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare direct-from-cpfp signing jobs: %w", err)
		}

		directFromCpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: directFromCpfpSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to sign direct-from-cpfp refund txs: %w", err)
		}

		directFromCpfpLeafSigningJobs, err := prepareLeafSigningJobs(
			leaves, directFromCpfpRefundTxs, directFromCpfpSigningResults.Results, directFromCpfpUserCommitments, directFromCpfpSigningCommitments)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare direct-from-cpfp leaf signing jobs: %w", err)
		}

		// 3. Direct refund txs (spends from DirectTx) - only for leaves that have DirectTx
		var directLeafSigningJobs []*pb.UserSignedTxSigningJob
		leavesWithDirect := filterLeavesWithDirectTx(leaves)
		if len(leavesWithDirect) > 0 {
			directSigningJobsFiltered, directRefundTxs, directUserCommitments, err := prepareFrostSigningJobsForDirectRefund(
				leavesWithDirect, directSigningCommitments[:len(leavesWithDirect)], receiverIdentityPubKey)
			if err != nil {
				return nil, fmt.Errorf("failed to prepare direct signing jobs: %w", err)
			}

			directSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
				SigningJobs: directSigningJobsFiltered,
				Role:        pbfrost.SigningRole_USER,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to sign direct refund txs: %w", err)
			}

			directLeafSigningJobs, err = prepareLeafSigningJobs(
				leavesWithDirect, directRefundTxs, directSigningResults.Results, directUserCommitments, directSigningCommitments[:len(leavesWithDirect)])
			if err != nil {
				return nil, fmt.Errorf("failed to prepare direct leaf signing jobs: %w", err)
			}
		}

		userSignedTransfer = &pb.StartUserSignedTransferRequest{
			TransferId:                 transferID.String(),
			OwnerIdentityPublicKey:     config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey:  receiverIdentityPubKey.Serialize(),
			LeavesToSend:               cpfpLeafSigningJobs,
			DirectFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
			DirectLeavesToSend:         directLeafSigningJobs,
			ExpiryTime:                 timestamppb.New(expireTime),
		}
	} else {
		// For SEND, use HTLC transactions
		originalRefundSigningCommitments := signingCommitments.SigningCommitments[:len(leaves)]
		signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(
			leaves, originalRefundSigningCommitments, receiverIdentityPubKey, keys.Public{})
		if err != nil {
			return nil, err
		}

		signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: signingJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, err
		}

		leafSigningJobs, err := prepareLeafSigningJobs(
			leaves, refundTxs, signingResults.Results, userCommitments, originalRefundSigningCommitments)
		if err != nil {
			return nil, err
		}

		htlcSigningCommitments := signingCommitments.SigningCommitments[len(leaves):]
		transfer, err = buildLightningHTLCTransfer(ctx, leaves, transferID, config, receiverIdentityPubKey, htlcSigningCommitments, paymentHash, signerConn, expireTime)
		if err != nil {
			return nil, fmt.Errorf("unable to build lightning htlc transfer: %w", err)
		}

		userSignedTransfer = &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(expireTime),
		}
	}

	response, err := client.InitiatePreimageSwapV2(tmpCtx, &pb.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash,
		Reason:      reason,
		InvoiceAmount: &pb.InvoiceAmount{
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: bolt11String,
			},
			ValueSats: amountSats,
		},
		Transfer:                  userSignedTransfer,
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		FeeSats:                   feeSats,
		TransferRequest:           transfer,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func buildLightningHTLCTransfer(
	ctx context.Context,
	leaves []LeafKeyTweak,
	transferID uuid.UUID,
	config *TestWalletConfig,
	receiverIdentityPubKey keys.Public,
	htlcSigningCommitments []*pb.RequestedSigningCommitments,
	paymentHash []byte,
	signerConn *grpc.ClientConn,
	expireTime time.Time,
) (*pb.StartTransferRequest, error) {
	if len(htlcSigningCommitments) != len(leaves)*3 {
		return nil, fmt.Errorf("number of htlc signing commitments does not match number of leaves")
	}

	cpfpRefundSigningCommitments := htlcSigningCommitments[:len(leaves)]
	directRefundSigningCommitments := htlcSigningCommitments[len(leaves) : len(leaves)*2]
	directFromCpfpRefundSigningCommitments := htlcSigningCommitments[len(leaves)*2:]
	// Build refund htlc transactions
	refundSigningJobs, refundTxs, refundUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(
		leaves,
		cpfpRefundSigningCommitments,
		receiverIdentityPubKey,
		config.IdentityPublicKey(),
		PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund,
		config.Network,
		paymentHash,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare frost signing jobs for user signed refund htlc: %w", err)
	}

	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	cpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: refundSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	refundLeafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		refundTxs,
		cpfpSigningResults.Results,
		refundUserCommitments,
		cpfpRefundSigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// Build direct htlc transactions from cpfp node tx
	directFromCpfpSigningJobs, directFromCpfpRefundTxs, directFromCpfpUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(
		leaves,
		directFromCpfpRefundSigningCommitments,
		receiverIdentityPubKey,
		config.IdentityPublicKey(),
		PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund,
		config.Network,
		paymentHash,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare frost signing jobs for user signed refund htlc: %w", err)
	}

	directFromCpfpSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: directFromCpfpSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	directFromCpfpLeafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		directFromCpfpRefundTxs,
		directFromCpfpSigningResults.Results,
		directFromCpfpUserCommitments,
		directFromCpfpRefundSigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// Build direct htlc transactions from direct node tx
	var directRefundLeafSigningJobs []*pb.UserSignedTxSigningJob
	directRefundSigningJobs, directRefundTxs, directRefundUserCommitments, err := prepareFrostSigningJobsForUserSignedRefundHTLC(leaves, directRefundSigningCommitments, receiverIdentityPubKey, config.IdentityPublicKey(), PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund, config.Network, paymentHash)
	if err == nil {
		directRefundSigningResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
			SigningJobs: directRefundSigningJobs,
			Role:        pbfrost.SigningRole_USER,
		})
		if err != nil {
			return nil, err
		}
		directRefundLeafSigningJobs, err = prepareLeafSigningJobs(
			leaves,
			directRefundTxs,
			directRefundSigningResults.Results,
			directRefundUserCommitments,
			directRefundSigningCommitments,
		)
		if err != nil {
			return nil, err
		}
	}

	keyTweakInputMap, err := PrepareSendTransferKeyTweaks(config, transferID, receiverIdentityPubKey, leaves, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to prepare send transfer key tweaks: %w", err)
	}

	encryptedKeyTweaks := make(map[string][]byte)
	for identifier, keyTweaks := range keyTweakInputMap {
		protoToEncrypt := pb.SendLeafKeyTweaks{
			LeavesToSend: keyTweaks,
		}
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
		LeavesToSend:               refundLeafSigningJobs,
		DirectLeavesToSend:         directRefundLeafSigningJobs,
		DirectFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
		KeyTweakPackage:            encryptedKeyTweaks,
	}

	transferPackageSigningPayload := common.GetTransferPackageSigningPayload(transferID, transferPackage)
	signature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), transferPackageSigningPayload)
	transferPackage.UserSignature = signature.Serialize()

	return &pb.StartTransferRequest{
		TransferId:                transferID.String(),
		OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		TransferPackage:           transferPackage,
		ExpiryTime:                timestamppb.New(expireTime),
	}, nil
}

// filterLeavesWithDirectTx returns only leaves that have a DirectTx set.
func filterLeavesWithDirectTx(leaves []LeafKeyTweak) []LeafKeyTweak {
	var result []LeafKeyTweak
	for _, leaf := range leaves {
		if len(leaf.Leaf.DirectTx) > 0 {
			result = append(result, leaf)
		}
	}
	return result
}
