package wallet

import (
	"bytes"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/objects"
)

// CreateUserKeyPackage creates a user frost signing key package from a signing private key.
func CreateUserKeyPackage(signingPrivateKey []byte) *pbfrost.KeyPackage {
	userIdentifier := "0000000000000000000000000000000000000000000000000000000000000063"
	pubkey := secp256k1.PrivKeyFromBytes(signingPrivateKey).PubKey()
	userKeyPackage := &pbfrost.KeyPackage{
		Identifier:  userIdentifier,
		SecretShare: signingPrivateKey,
		PublicShares: map[string][]byte{
			userIdentifier: pubkey.SerializeCompressed(),
		},
		PublicKey:  pubkey.SerializeCompressed(),
		MinSigners: 1,
	}
	return userKeyPackage
}

func prepareFrostSigningJobsForUserSignedRefund(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubkey *secp256k1.PublicKey,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*objects.SigningCommitment, error) {
	signingJobs := []*pbfrost.FrostSigningJob{}
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*objects.SigningCommitment, len(leaves))
	for i, leaf := range leaves {
		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse node tx: %v", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}
		nextSequence, err := spark.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get next sequence: %v", err)
		}
		amountSats := nodeTx.TxOut[0].Value
		cpfpRefundTx, _, err := createRefundTxs(nextSequence, &nodeOutPoint, amountSats, receiverIdentityPubkey, false)
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
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %v", err)
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
	leafSigningJobs := []*pb.UserSignedTxSigningJob{}
	for i, leaf := range leaves {
		userCommitmentProto, err := userCommitments[i].MarshalProto()
		if err != nil {
			return nil, err
		}
		leafSigningJobs = append(leafSigningJobs, &pb.UserSignedTxSigningJob{
			LeafId:                 leaf.Leaf.Id,
			SigningPublicKey:       leaf.SigningPrivKey,
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
