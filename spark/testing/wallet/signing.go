package wallet

import (
	"bytes"
	"fmt"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
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
) ([]*pbfrost.FrostSigningJob, [][]byte, []*objects.SigningCommitment, error) {
	var signingJobs []*pbfrost.FrostSigningJob
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*objects.SigningCommitment, len(leaves))
	if len(leaves) != len(signingCommitments) {
		return nil, nil, nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}
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
		nextSequence, err := spark.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		amountSats := nodeTx.TxOut[0].Value
		cpfpRefundTx, _, err := createRefundTxs(nextSequence, &nodeOutPoint, amountSats, receiverIdentityPubKey, false)
		if err != nil {
			return nil, nil, nil, err
		}
		var refundBuf bytes.Buffer
		err = cpfpRefundTx.Serialize(&refundBuf)
		if err != nil {
			return nil, nil, nil, err
		}
		refundTxs[i] = refundBuf.Bytes()

		sighash, err := common.SigHashFromTx(cpfpRefundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
		}

		signingNonce, err := objects.RandomSigningNonce()
		if err != nil {
			return nil, nil, nil, err
		}
		signingNonceProto, err := signingNonce.MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitments[i] = signingNonce.SigningCommitment()

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
	network common.Network,
	paymentHash []byte,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*objects.SigningCommitment, error) {
	var signingJobs []*pbfrost.FrostSigningJob
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*objects.SigningCommitment, len(leaves))
	if len(leaves) != len(signingCommitments) {
		return nil, nil, nil, fmt.Errorf("mismatched lengths: leaves: %d, commitments: %d", len(leaves), len(signingCommitments))
	}
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
			if len(leaf.Leaf.DirectRefundTx) == 0 {
				return nil, nil, nil, fmt.Errorf("direct refund tx is empty for leaf id: %s", leaf.Leaf.Id)
			}
			nodeTx, err = common.TxFromRawTxBytes(leaf.Leaf.DirectRefundTx)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to parse direct refund tx: %w", err)
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

		fmt.Printf("nodeTx: %s\n", nodeTx.TxHash().String())

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

		signingNonce, err := objects.RandomSigningNonce()
		if err != nil {
			return nil, nil, nil, err
		}
		signingNonceProto, err := signingNonce.MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitments[i] = signingNonce.SigningCommitment()

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
	userCommitments []*objects.SigningCommitment,
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
		userCommitmentProto, err := userCommitments[i].MarshalProto()
		if err != nil {
			return nil, err
		}
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
