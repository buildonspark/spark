package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/wire"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	"github.com/lightsparkdev/spark/common/sighash"
	pbcommon "github.com/lightsparkdev/spark/proto/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/frost"
	transferpkg "github.com/lightsparkdev/spark/so/transfer"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// mpcRefundVariant names one of the three refund transactions every transfer
// submission must carry.
type mpcRefundVariant int

const (
	mpcVariantCPFP mpcRefundVariant = iota
	mpcVariantDirect
	mpcVariantDirectFromCPFP
)

// mpcSubUserLeaf is the simulated sub-user group's per-leaf secret state. The
// test process plays every sub-user, so it derives all per-position material
// centrally; the wire artifacts are identical to what independently-running
// sub-users would produce.
type mpcSubUserLeaf struct {
	leaf LeafKeyTweak
	// The sub-users' Shamir shares of the leaf owner signing key, by
	// position; these drive the FROST refund contributions.
	signingShares map[uint32]curve.Scalar
	// Each sub-user's resharing polynomial for its share of the key tweak
	// (owner key minus mask), by position; constant term is the tweak share.
	resharePolys map[uint32]*polynomial.ScalarPolynomial
}

// SendTransferMpc drives one full multiparty send through the public
// start_transfer_mpc endpoint: it Shamir-splits each leaf's owner signing key
// and key tweak across the given sub-user positions, produces live FROST
// refund contributions through the local signer, reshares and seals the tweak
// sub-shares to every operator, and submits the group-signed authorization.
// leaf.NewSigningPrivKey plays the transfer mask (the receiver's new leaf
// signing key), exactly as in the single-party send.
func SendTransferMpc(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiver keys.Public,
	expiryTime time.Time,
	positions []uint32,
) (*pb.Transfer, error) {
	sparkConn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	token, err := AuthenticateWithConnection(ctx, config, sparkConn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)
	client := pb.NewSparkServiceClient(sparkConn)

	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}

	req, err := CreateStartTransferMpcRequest(authCtx, transferID, config, client, leaves, receiver, expiryTime, positions)
	if err != nil {
		return nil, err
	}

	resp, err := client.StartTransferMpc(authCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to start MPC transfer: %w", err)
	}
	return resp.GetTransfer(), nil
}

// CreateStartTransferMpcRequest builds a complete multiparty send submission
// without sending it, so tests can perturb it before submitting.
func CreateStartTransferMpcRequest(
	ctx context.Context,
	transferID uuid.UUID,
	config *TestWalletConfig,
	client pb.SparkServiceClient,
	leaves []LeafKeyTweak,
	receiver keys.Public,
	expiryTime time.Time,
	positions []uint32,
) (*pb.StartTransferMpcRequest, error) {
	if len(positions) == 0 || !slices.IsSorted(positions) {
		return nil, fmt.Errorf("positions must be non-empty and ascending, got %v", positions)
	}
	// Position 0 evaluates both sharing polynomials at their constant term,
	// which would put the leaf owner signing key itself on the wire as a
	// "share". Duplicates and the participant cap stay unchecked here so this
	// builder can still submit them for the operator to reject.
	if positions[0] == 0 {
		return nil, fmt.Errorf("positions must be >= 1, got %v", positions)
	}

	const refundTxsPerLeaf = 3
	allNodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		allNodeIDs[i] = leaf.Leaf.GetId()
	}
	signingCommitmentsResp, err := client.GetSigningCommitments(ctx, &pb.GetSigningCommitmentsRequest{
		NodeIds: allNodeIDs,
		Count:   refundTxsPerLeaf,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get signing commitments: %w", err)
	}
	commitmentsByLeafID := extractCommitmentsByLeaf(leaves, signingCommitmentsResp.GetSigningCommitments())

	subUserLeaves := make([]*mpcSubUserLeaf, len(leaves))
	for i, leaf := range leaves {
		subUserLeaves[i], err = newMpcSubUserLeaf(config, leaf, positions)
		if err != nil {
			return nil, fmt.Errorf("leaf %s: %w", leaf.Leaf.GetId(), err)
		}
	}

	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()
	signerClient := pbfrost.NewFrostServiceClient(signerConn)

	variants := []mpcRefundVariant{mpcVariantCPFP, mpcVariantDirect, mpcVariantDirectFromCPFP}
	jobsByVariant := make(map[mpcRefundVariant][]*pb.UserSignedTxSigningJob, len(variants))
	sighashesByLeaf := make(map[string]map[mpcRefundVariant][]byte, len(leaves))
	for _, leaf := range leaves {
		sighashesByLeaf[leaf.Leaf.GetId()] = make(map[mpcRefundVariant][]byte, len(variants))
	}
	for _, variant := range variants {
		jobs, err := signMpcRefundVariant(ctx, signerClient, subUserLeaves, commitmentsByLeafID, receiver, positions, variant, sighashesByLeaf)
		if err != nil {
			return nil, err
		}
		jobsByVariant[variant] = jobs
	}

	var pubLeaves []*pb.MpcSendLeaf
	var authLeaves []*pb.LeafAuthorization
	var leafSighashes []transferpkg.MpcLeafRefundSighashes
	sealedPayloads := make(map[string]map[uint32]*pb.MpcSealedSharePayload, len(config.SigningOperators))
	for identifier := range config.SigningOperators {
		perPosition := make(map[uint32]*pb.MpcSealedSharePayload, len(positions))
		for _, position := range positions {
			perPosition[position] = &pb.MpcSealedSharePayload{TransferId: transferID.String()}
		}
		sealedPayloads[identifier] = perPosition
	}

	receiverEciesPubKey, err := eciesgo.NewPublicKeyFromBytes(receiver.Serialize())
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver public key: %w", err)
	}

	for _, sub := range subUserLeaves {
		leafID := sub.leaf.Leaf.GetId()
		leafUUID, err := uuid.Parse(leafID)
		if err != nil {
			return nil, fmt.Errorf("invalid leaf id %s: %w", leafID, err)
		}

		commitments := make([]*pb.SubUserCommitment, len(positions))
		for j, position := range positions {
			poly := sub.resharePolys[position]
			proofs := make([][]byte, len(poly.Coefs))
			for k, coef := range poly.Coefs {
				pub, err := coef.Point().ToPublicKey()
				if err != nil {
					return nil, fmt.Errorf("leaf %s position %d: %w", leafID, position, err)
				}
				proofs[k] = pub.Serialize()
			}
			commitments[j] = &pb.SubUserCommitment{Proofs: proofs}

			for identifier, operator := range config.SigningOperators {
				operatorX := curve.ScalarFromInt(uint32(operator.ID + 1))
				sealedPayloads[identifier][position].LeafShares = append(
					sealedPayloads[identifier][position].LeafShares,
					&pb.MpcLeafSubShare{
						LeafId:      leafID,
						SecretShare: poly.Eval(operatorX).Serialize(),
					},
				)
			}
		}

		secretCipher, err := eciesgo.Encrypt(receiverEciesPubKey, sub.leaf.NewSigningPrivKey.Serialize())
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt mask for leaf %s: %w", leafID, err)
		}
		payload := append(append([]byte(leafID), []byte(transferID.String())...), secretCipher...)
		payloadHash := sha256.Sum256(payload)
		leafSignature := ecdsa.Sign(config.IdentityPrivateKey.ToBTCEC(), payloadHash[:])

		pubLeaves = append(pubLeaves, &pb.MpcSendLeaf{
			LeafId:             leafID,
			SubuserCommitments: commitments,
			SecretCipher:       secretCipher,
			Signature: &pbcommon.Signature{
				Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
				Signature: leafSignature.Serialize(),
			},
		})
		authLeaves = append(authLeaves, &pb.LeafAuthorization{
			LeafId:                    leafID,
			AmountSats:                sub.leaf.Leaf.GetValue(),
			OwnerSigningPublicKey:     sub.leaf.SigningPrivKey.Public().Serialize(),
			MaskCommitment:            sub.leaf.NewSigningPrivKey.Public().Serialize(),
			ReceiverIdentityPublicKey: receiver.Serialize(),
		})
		leafSighashes = append(leafSighashes, transferpkg.MpcLeafRefundSighashes{
			LeafID:         leafUUID,
			CPFP:           sighashesByLeaf[leafID][mpcVariantCPFP],
			Direct:         sighashesByLeaf[leafID][mpcVariantDirect],
			DirectFromCPFP: sighashesByLeaf[leafID][mpcVariantDirectFromCPFP],
		})
	}

	keyTweaks := make(map[string]*pb.MpcOperatorShares, len(config.SigningOperators))
	for identifier := range config.SigningOperators {
		operatorPubKey, err := eciesgo.NewPublicKeyFromBytes(config.SigningOperators[identifier].IdentityPublicKey.Serialize())
		if err != nil {
			return nil, fmt.Errorf("failed to parse operator %s identity key: %w", identifier, err)
		}
		shares := make([]*pb.MpcSealedShare, len(positions))
		for j, position := range positions {
			plaintext, err := proto.Marshal(sealedPayloads[identifier][position])
			if err != nil {
				return nil, fmt.Errorf("failed to marshal sealed payload: %w", err)
			}
			sealed, err := eciesgo.Encrypt(operatorPubKey, plaintext)
			if err != nil {
				return nil, fmt.Errorf("failed to seal shares for operator %s: %w", identifier, err)
			}
			shares[j] = &pb.MpcSealedShare{Ecies: sealed}
		}
		keyTweaks[identifier] = &pb.MpcOperatorShares{Shares: shares}
	}

	req := &pb.StartTransferMpcRequest{
		TransferId:             transferID.String(),
		OwnerIdentityPublicKey: config.IdentityPublicKey().Serialize(),
		MpcTransferPackage: &pb.MpcTransferPackage{
			Positions:                  positions,
			Leaves:                     pubLeaves,
			KeyTweaks:                  keyTweaks,
			LeavesToSend:               jobsByVariant[mpcVariantCPFP],
			DirectLeavesToSend:         jobsByVariant[mpcVariantDirect],
			DirectFromCpfpLeavesToSend: jobsByVariant[mpcVariantDirectFromCPFP],
			Authorization: &pb.TransferAuthorization{
				TransferId:            transferID.String(),
				Leaves:                authLeaves,
				RefundSighashesDigest: transferpkg.MpcRefundSighashesDigest(leafSighashes),
				// The authorization payload binds expiry as whole seconds.
				ExpiryTime: timestamppb.New(expiryTime.Truncate(time.Second)),
			},
		},
	}
	if err := SignMpcAuthorization(req, config.IdentityPrivateKey); err != nil {
		return nil, err
	}
	return req, nil
}

// SignMpcAuthorization signs the request's TransferAuthorization with the
// sender group's identity key (ECDSA). The payload does not cover the
// signature itself, but parsing requires a plausible one, so it signs in two
// passes through the operator-side parser — guaranteeing byte-exactness with
// what operators verify.
func SignMpcAuthorization(req *pb.StartTransferMpcRequest, identityKey keys.Private) error {
	auth := req.GetMpcTransferPackage().GetAuthorization()
	auth.Signature = &pbcommon.Signature{
		Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
		Signature: []byte{0x30, 0x01},
	}
	parsed, err := transferpkg.ParseMpcSubmission(req)
	if err != nil {
		return fmt.Errorf("failed to parse own submission: %w", err)
	}
	signature := ecdsa.Sign(identityKey.ToBTCEC(), parsed.AuthorizationPayload())
	auth.Signature = &pbcommon.Signature{
		Scheme:    pbcommon.SignatureScheme_SIGNATURE_SCHEME_ECDSA,
		Signature: signature.Serialize(),
	}
	return nil
}

// newMpcSubUserLeaf Shamir-splits the leaf's owner signing key and key tweak
// across the sub-user positions (threshold = all listed positions), and
// builds each sub-user's committed resharing polynomial for its tweak share
// (degree = operator threshold - 1).
func newMpcSubUserLeaf(config *TestWalletConfig, leaf LeafKeyTweak, positions []uint32) (*mpcSubUserLeaf, error) {
	owner, err := curve.ParseScalar(leaf.SigningPrivKey.Serialize())
	if err != nil {
		return nil, fmt.Errorf("failed to parse owner signing key: %w", err)
	}
	mask, err := curve.ParseScalar(leaf.NewSigningPrivKey.Serialize())
	if err != nil {
		return nil, fmt.Errorf("failed to parse mask: %w", err)
	}
	tweak := owner.Sub(mask)

	signingPoly, err := randomPolynomialWithConstant(owner, len(positions))
	if err != nil {
		return nil, err
	}
	tweakPoly, err := randomPolynomialWithConstant(tweak, len(positions))
	if err != nil {
		return nil, err
	}

	operatorThreshold := config.Threshold
	signingShares := make(map[uint32]curve.Scalar, len(positions))
	resharePolys := make(map[uint32]*polynomial.ScalarPolynomial, len(positions))
	for _, position := range positions {
		x := curve.ScalarFromInt(position)
		signingShares[position] = signingPoly.Eval(x)
		reshare, err := randomPolynomialWithConstant(tweakPoly.Eval(x), operatorThreshold)
		if err != nil {
			return nil, err
		}
		resharePolys[position] = reshare
	}
	return &mpcSubUserLeaf{leaf: leaf, signingShares: signingShares, resharePolys: resharePolys}, nil
}

func randomPolynomialWithConstant(constant curve.Scalar, length int) (*polynomial.ScalarPolynomial, error) {
	coefs := make([]curve.Scalar, length)
	coefs[0] = constant
	for i := 1; i < length; i++ {
		scalar, err := curve.ParseScalar(keys.GeneratePrivateKey().Serialize())
		if err != nil {
			return nil, fmt.Errorf("failed to generate random scalar: %w", err)
		}
		coefs[i] = scalar
	}
	return &polynomial.ScalarPolynomial{Coefs: coefs}, nil
}

// mpcRefundTx rebuilds one refund variant with the stepped-down timelock,
// mirroring the single-party job builders, and returns its serialized bytes
// and BIP-341 sighash.
func mpcRefundTx(leaf LeafKeyTweak, receiver keys.Public, variant mpcRefundVariant) ([]byte, []byte, error) {
	nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.GetNodeTx())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse node tx: %w", err)
	}

	var refundTx *wire.MsgTx
	var parentTxOut *wire.TxOut
	switch variant {
	case mpcVariantCPFP, mpcVariantDirectFromCPFP:
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.GetRefundTx())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse refund tx: %w", err)
		}
		nextNodeSequence, nextDirectSequence, err := bitcointransaction.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		cpfpRefundTx, directRefundTx, err := CreateRefundTxs(
			nextNodeSequence, nextDirectSequence, &nodeOutPoint, nodeTx.TxOut[0].Value, receiver, variant == mpcVariantDirectFromCPFP)
		if err != nil {
			return nil, nil, err
		}
		refundTx = cpfpRefundTx
		if variant == mpcVariantDirectFromCPFP {
			refundTx = directRefundTx
		}
		parentTxOut = nodeTx.TxOut[0]
	case mpcVariantDirect:
		directTx, err := common.TxFromRawTxBytes(leaf.Leaf.GetDirectTx())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse direct tx: %w", err)
		}
		currDirectRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.GetDirectRefundTx())
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse direct refund tx: %w", err)
		}
		nextSequence, nextDirectSequence, err := bitcointransaction.NextSequence(currDirectRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get next sequence: %w", err)
		}
		nextSequence -= spark.DirectTimelockOffset
		directOutPoint := wire.OutPoint{Hash: directTx.TxHash(), Index: 0}
		_, directRefundTx, err := CreateRefundTxs(
			nextSequence, nextDirectSequence, &directOutPoint, directTx.TxOut[0].Value, receiver, true)
		if err != nil {
			return nil, nil, err
		}
		refundTx = directRefundTx
		parentTxOut = directTx.TxOut[0]
	default:
		return nil, nil, fmt.Errorf("unknown refund variant %d", variant)
	}

	var refundBuf bytes.Buffer
	if err := refundTx.Serialize(&refundBuf); err != nil {
		return nil, nil, err
	}
	txSighash, err := sighash.FromTx(refundTx, 0, parentTxOut)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to calculate sighash: %w", err)
	}
	return refundBuf.Bytes(), txSighash.Serialize(), nil
}

// signMpcRefundVariant builds one refund variant's signing jobs: it rebuilds
// each leaf's refund transaction, runs every sub-user's FROST round 1 locally
// and round 2 through the local signer (role USER, user-group scheme), and
// assembles the per-leaf jobs with the sub-user contributions in place of the
// single-signer pair. Sighashes are recorded into sighashesByLeaf for the
// authorization digest.
func signMpcRefundVariant(
	ctx context.Context,
	signerClient pbfrost.FrostServiceClient,
	subUserLeaves []*mpcSubUserLeaf,
	commitmentsByLeafID map[string][]*pb.RequestedSigningCommitments,
	receiver keys.Public,
	positions []uint32,
	variant mpcRefundVariant,
	sighashesByLeaf map[string]map[mpcRefundVariant][]byte,
) ([]*pb.UserSignedTxSigningJob, error) {
	commitmentIndex := map[mpcRefundVariant]int{
		mpcVariantCPFP:           0,
		mpcVariantDirect:         1,
		mpcVariantDirectFromCPFP: 2,
	}[variant]

	type leafSigningState struct {
		rawTx         []byte
		seCommitments map[string]*pbcommon.SigningCommitment
		nonces        map[uint32]*frost.SigningNonce
		subUserProtos []*pbfrost.SubUserCommitment
	}
	states := make([]*leafSigningState, len(subUserLeaves))

	var signingJobs []*pbfrost.FrostSigningJob
	for i, sub := range subUserLeaves {
		leafID := sub.leaf.Leaf.GetId()
		rawTx, txSighash, err := mpcRefundTx(sub.leaf, receiver, variant)
		if err != nil {
			return nil, fmt.Errorf("leaf %s: %w", leafID, err)
		}
		sighashesByLeaf[leafID][variant] = txSighash

		state := &leafSigningState{
			rawTx:         rawTx,
			seCommitments: commitmentsByLeafID[leafID][commitmentIndex].GetSigningNonceCommitments(),
			nonces:        make(map[uint32]*frost.SigningNonce, len(positions)),
		}
		for _, position := range positions {
			nonce := frost.GenerateSigningNonce()
			commitment := nonce.SigningCommitment()
			state.nonces[position] = &nonce
			state.subUserProtos = append(state.subUserProtos, &pbfrost.SubUserCommitment{
				Position:   position,
				Commitment: commitment.MarshalProto(),
			})
		}
		states[i] = state

		for _, position := range positions {
			share := sub.signingShares[position]
			sharePriv, err := keys.ParsePrivateKey(share.Serialize())
			if err != nil {
				return nil, fmt.Errorf("leaf %s position %d: %w", leafID, position, err)
			}
			identifier := fmt.Sprintf("%064x", position)
			signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
				JobId:   fmt.Sprintf("%s:%d:%d", leafID, variant, position),
				Message: txSighash,
				KeyPackage: &pbfrost.KeyPackage{
					Identifier:   identifier,
					SecretShare:  share.Serialize(),
					PublicShares: map[string][]byte{identifier: sharePriv.Public().Serialize()},
					PublicKey:    sub.leaf.Leaf.GetVerifyingPublicKey(),
					MinSigners:   uint32(len(positions)),
				},
				VerifyingKey:       sub.leaf.Leaf.GetVerifyingPublicKey(),
				Nonce:              state.nonces[position].MarshalProto(),
				Commitments:        state.seCommitments,
				SigningScheme:      pbfrost.SigningScheme_SIGNING_SCHEME_MPC_USER_GROUP,
				SubuserCommitments: state.subUserProtos,
			})
		}
	}

	// One batched round-2 call per variant; the sub-user commitment slices
	// are complete by now because rounds 1 and 2 were separated above.
	results, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, fmt.Errorf("sub-user round 2 failed (%d): %w", variant, err)
	}

	jobs := make([]*pb.UserSignedTxSigningJob, len(subUserLeaves))
	for i, sub := range subUserLeaves {
		leafID := sub.leaf.Leaf.GetId()
		state := states[i]
		contributions := make([]*pb.SubUserSigningContribution, len(positions))
		for j, position := range positions {
			result := results.GetResults()[fmt.Sprintf("%s:%d:%d", leafID, variant, position)]
			if result == nil {
				return nil, fmt.Errorf("missing round-2 result for leaf %s position %d", leafID, position)
			}
			contributions[j] = &pb.SubUserSigningContribution{
				NonceCommitment:  state.subUserProtos[j].GetCommitment(),
				PartialSignature: result.GetSignatureShare(),
			}
		}
		jobs[i] = &pb.UserSignedTxSigningJob{
			LeafId:           leafID,
			SigningPublicKey: sub.leaf.SigningPrivKey.Public().Serialize(),
			RawTx:            state.rawTx,
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: state.seCommitments,
			},
			SubuserContributions: contributions,
		}
	}
	return jobs, nil
}
