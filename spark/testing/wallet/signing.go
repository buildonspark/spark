package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/so/frost"

	"github.com/btcsuite/btcd/wire"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
)

const (
	HTLCSequenceOffset   = 30
	DirectSequenceOffset = 15
)

// CreateUserKeyPackage creates a user frost signing key package from a signing private key.
func CreateUserKeyPackage(signingPrivateKey keys.Private) *pbfrost.KeyPackage {
	const userIdentifier = "0000000000000000000000000000000000000000000000000000000000000063"
	pubKeyBytes := signingPrivateKey.Public().Serialize()
	userKeyPackage := &pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: signingPrivateKey.Serialize(),
		PublicShares: map[string][]byte{
			userIdentifier: pubKeyBytes,
		},
		PublicKey:  pubKeyBytes,
		MinSigners: 1,
	}
	return userKeyPackage
}

func prepareFrostSigningJobsForUserSignedRefund(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubKey keys.Public,
	adaptorPublicKey keys.Public,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*frost.SigningCommitment, error) {
	return prepareFrostSigningJobsForUserSignedRefundWithType(leaves, signingCommitments, receiverIdentityPubKey, true, adaptorPublicKey)
}

// prepareFrostSigningJobsForUserSignedRefundDirect creates signing jobs for direct refund transactions (with fee deduction)
// This is used for DirectFromCPFP path, which spends from NodeTx
func prepareFrostSigningJobsForUserSignedRefundDirect(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubKey keys.Public,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*frost.SigningCommitment, error) {
	return prepareFrostSigningJobsForUserSignedRefundWithType(leaves, signingCommitments, receiverIdentityPubKey, false, keys.Public{})
}

// prepareFrostSigningJobsForDirectRefund creates signing jobs for direct refund transactions
// This is used for Direct path, which spends from DirectTx (not NodeTx)
func prepareFrostSigningJobsForDirectRefund(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubKey keys.Public,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*frost.SigningCommitment, error) {
	if len(leaves) != len(signingCommitments) {
		return nil, nil, nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}
	var signingJobs []*pbfrost.FrostSigningJob
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*frost.SigningCommitment, len(leaves))
	for i, leaf := range leaves {
		// Parse DirectTx (the parent transaction for direct refunds)
		directTx, err := common.TxFromRawTxBytes(leaf.Leaf.DirectTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse direct tx: %w", err)
		}
		directOutPoint := wire.OutPoint{Hash: directTx.TxHash(), Index: 0}

		// Parse current DirectRefundTx to get the timelock
		currDirectRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.DirectRefundTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse direct refund tx: %w", err)
		}
		nextSequence, nextDirectSequence, err := bitcointransaction.NextSequence(currDirectRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		nextSequence -= spark.DirectTimelockOffset
		amountSats := directTx.TxOut[0].Value

		// Create new direct refund tx with shorter timelock
		_, directRefundTx, err := CreateRefundTxs(nextSequence, nextDirectSequence, &directOutPoint, amountSats, receiverIdentityPubKey, true)
		if err != nil {
			return nil, nil, nil, err
		}

		var refundBuf bytes.Buffer
		err = directRefundTx.Serialize(&refundBuf)
		if err != nil {
			return nil, nil, nil, err
		}
		refundTxs[i] = refundBuf.Bytes()

		sighash, err := common.SigHashFromTx(directRefundTx, 0, directTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
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
	return signingJobs, refundTxs, userCommitments, nil
}

func prepareFrostSigningJobsForUserSignedRefundWithType(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubKey keys.Public,
	useCPFP bool,
	adaptorPublicKey keys.Public,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*frost.SigningCommitment, error) {
	if len(leaves) != len(signingCommitments) {
		return nil, nil, nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}
	var signingJobs []*pbfrost.FrostSigningJob
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*frost.SigningCommitment, len(leaves))
	for i, leaf := range leaves {
		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse node tx: %w", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}

		nextNodeSequence, nextDirectSequence, err := bitcointransaction.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}

		amountSats := nodeTx.TxOut[0].Value

		var refundTx *wire.MsgTx
		if useCPFP {
			cpfpRefundTx, _, err := CreateRefundTxs(nextNodeSequence, nextDirectSequence, &nodeOutPoint, amountSats, receiverIdentityPubKey, false)
			if err != nil {
				return nil, nil, nil, err
			}
			refundTx = cpfpRefundTx
		} else {
			_, directRefundTx, err := CreateRefundTxs(nextNodeSequence, nextDirectSequence, &nodeOutPoint, amountSats, receiverIdentityPubKey, true)
			if err != nil {
				return nil, nil, nil, err
			}
			refundTx = directRefundTx
		}

		var refundBuf bytes.Buffer
		err = refundTx.Serialize(&refundBuf)
		if err != nil {
			return nil, nil, nil, err
		}
		refundTxs[i] = refundBuf.Bytes()

		sighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
		}

		signingNonce := frost.GenerateSigningNonce()
		signingNonceProto, _ := signingNonce.MarshalProto()
		signingCommitment := signingNonce.SigningCommitment()
		userCommitments[i] = &signingCommitment
		userCommitmentProto, _ := signingCommitment.MarshalProto()

		userKeyPackage := CreateUserKeyPackage(leaf.SigningPrivKey)

		signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
			JobId:            leaf.Leaf.Id,
			Message:          sighash,
			KeyPackage:       userKeyPackage,
			VerifyingKey:     leaf.Leaf.VerifyingPublicKey,
			Nonce:            signingNonceProto,
			Commitments:      signingCommitments[i].SigningNonceCommitments,
			UserCommitments:  userCommitmentProto,
			AdaptorPublicKey: adaptorPublicKey.Serialize(),
		})
	}
	return signingJobs, refundTxs, userCommitments, nil
}

type PrepareFrostSigningJobsForUserSignedRefundHTLCType int

const (
	PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund PrepareFrostSigningJobsForUserSignedRefundHTLCType = iota
	PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund
	PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund
)

func prepareFrostSigningJobsForUserSignedRefundHTLC(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubKey keys.Public,
	senderIdentityPubKey keys.Public,
	htlcType PrepareFrostSigningJobsForUserSignedRefundHTLCType,
	network btcnetwork.Network,
	paymentHash []byte,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*frost.SigningCommitment, error) {
	if len(leaves) != len(signingCommitments) {
		return nil, nil, nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}
	var signingJobs []*pbfrost.FrostSigningJob
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*frost.SigningCommitment, len(leaves))
	for i, leaf := range leaves {
		var nodeTx *wire.MsgTx
		var err error
		switch htlcType {
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund:
			nodeTx, err = common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to parse direct from cpfp refund tx: %w", err)
			}
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund:
			nodeTx, err = common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to parse direct from cpfp refund tx: %w", err)
			}
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund:
			if len(leaf.Leaf.DirectTx) == 0 {
				return nil, nil, nil, fmt.Errorf("direct tx is empty for leaf id: %s", leaf.Leaf.Id)
			}
			nodeTx, err = common.TxFromRawTxBytes(leaf.Leaf.DirectTx)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to parse direct tx: %w", err)
			}
		}

		refundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}

		var nextSequence uint32
		switch htlcType {
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund:
			nextSequence = refundTx.TxIn[0].Sequence - HTLCSequenceOffset
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund:
			nextSequence = refundTx.TxIn[0].Sequence - DirectSequenceOffset
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund:
			nextSequence = refundTx.TxIn[0].Sequence - DirectSequenceOffset
		}

		var htlcTx *wire.MsgTx
		switch htlcType {
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeCPFPRefund:
			htlcTx, err = bitcointransaction.CreateLightningHTLCTransaction(nodeTx, 0, network, nextSequence, paymentHash, receiverIdentityPubKey, senderIdentityPubKey)
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectRefund:
			htlcTx, err = bitcointransaction.CreateDirectLightningHTLCTransaction(nodeTx, 0, network, nextSequence, paymentHash, receiverIdentityPubKey, senderIdentityPubKey)
		case PrepareFrostSigningJobsForUserSignedRefundHTLCTypeDirectFromCpfpRefund:
			htlcTx, err = bitcointransaction.CreateDirectLightningHTLCTransaction(nodeTx, 0, network, nextSequence, paymentHash, receiverIdentityPubKey, senderIdentityPubKey)
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create lightning htlc transaction: %w", err)
		}
		var serializedTx bytes.Buffer
		err = htlcTx.Serialize(&serializedTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to serialize tx: %w", err)
		}
		refundTxs[i] = serializedTx.Bytes()

		sighash, err := common.SigHashFromTx(htlcTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
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
	return signingJobs, refundTxs, userCommitments, nil
}

func prepareLeafSigningJobs(
	leaves []LeafKeyTweak,
	refundTxs [][]byte,
	signingResults map[string]*pbcommon.SigningResult,
	userCommitments []*frost.SigningCommitment,
	signingCommitments []*pb.RequestedSigningCommitments,
) ([]*pb.UserSignedTxSigningJob, error) {
	if len(leaves) != len(refundTxs) {
		return nil, fmt.Errorf("mismatched lengths: leaves: %d, refund txs: %d", len(leaves), len(refundTxs))
	}
	if len(leaves) != len(signingResults) {
		return nil, fmt.Errorf("mismatched lengths: leaves: %d, results: %d", len(leaves), len(signingResults))
	}
	if len(leaves) != len(userCommitments) {
		return nil, fmt.Errorf("mismatched lengths: leaves: %d, user commitments: %d", len(leaves), len(userCommitments))
	}
	if len(leaves) != len(signingCommitments) {
		return nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}

	var leafSigningJobs []*pb.UserSignedTxSigningJob
	for i, leaf := range leaves {
		userCommitmentProto, _ := userCommitments[i].MarshalProto()
		leafSigningJobs = append(leafSigningJobs, &pb.UserSignedTxSigningJob{
			LeafId:                 leaf.Leaf.Id,
			SigningPublicKey:       leaf.SigningPrivKey.Public().Serialize(),
			RawTx:                  refundTxs[i],
			SigningNonceCommitment: userCommitmentProto,
			UserSignature:          signingResults[leaf.Leaf.Id].SignatureShare,
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: signingCommitments[i].SigningNonceCommitments,
			},
		})
	}
	return leafSigningJobs, nil
}

// CreateSigningJobFromTx creates a SigningJob from a transaction and signing private key,
// generating a new signing nonce and appending it to the provided signingNonces slice.
func CreateSigningJobFromTx(
	tx *wire.MsgTx,
	signingPrivateKey keys.Private,
	signingNonces []*frost.SigningNonce,
) (*pb.SigningJob, []*frost.SigningNonce, error) {
	var txBuf bytes.Buffer
	if err := tx.Serialize(&txBuf); err != nil {
		return nil, signingNonces, err
	}

	signingNonce := frost.GenerateSigningNonce()

	signingNonceCommitment, _ := signingNonce.SigningCommitment().MarshalProto()

	signingNonces = append(signingNonces, &signingNonce)
	signingJob := &pb.SigningJob{
		SigningPublicKey:       signingPrivateKey.Public().Serialize(),
		RawTx:                  txBuf.Bytes(),
		SigningNonceCommitment: signingNonceCommitment,
	}

	return signingJob, signingNonces, nil
}

// SignTransactionWithFrost signs a transaction using FROST, returning the aggregated signature.
func SignTransactionWithFrost(
	ctx context.Context,
	frostClient pbfrost.FrostServiceClient,
	rawTxBytes []byte,
	prevTxOut *wire.TxOut,
	signingNonce *frost.SigningNonce,
	signingPrivateKey keys.Private,
	verificationKey keys.Public,
	signingResult *pb.SigningResult,
) ([]byte, error) {
	tx, err := common.TxFromRawTxBytes(rawTxBytes)
	if err != nil {
		return nil, err
	}

	sighash, err := common.SigHashFromTx(tx, 0, prevTxOut)
	if err != nil {
		return nil, err
	}

	signingNonceCommitment, _ := signingNonce.SigningCommitment().MarshalProto()
	signingNonceProto, _ := signingNonce.MarshalProto()

	keyPackage := CreateUserKeyPackage(signingPrivateKey)
	signingJob := &pbfrost.FrostSigningJob{
		JobId:           uuid.NewString(),
		Message:         sighash,
		KeyPackage:      keyPackage,
		VerifyingKey:    verificationKey.Serialize(),
		Nonce:           signingNonceProto,
		Commitments:     signingResult.SigningNonceCommitments,
		UserCommitments: signingNonceCommitment,
	}

	response, err := frostClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: []*pbfrost.FrostSigningJob{signingJob},
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	aggResponse, err := frostClient.AggregateFrost(ctx, &pbfrost.AggregateFrostRequest{
		Message:            sighash,
		SignatureShares:    signingResult.SignatureShares,
		PublicShares:       signingResult.PublicKeys,
		VerifyingKey:       verificationKey.Serialize(),
		Commitments:        signingResult.SigningNonceCommitments,
		UserCommitments:    signingNonceCommitment,
		UserPublicKey:      verificationKey.Serialize(),
		UserSignatureShare: response.Results[signingJob.JobId].SignatureShare,
	})
	if err != nil {
		return nil, err
	}

	return aggResponse.Signature, nil
}
