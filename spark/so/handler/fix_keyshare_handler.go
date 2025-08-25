package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common/keys"
	secretsharing "github.com/lightsparkdev/spark/common/secret_sharing"
	"github.com/lightsparkdev/spark/common/secret_sharing/curve"
	"github.com/lightsparkdev/spark/common/secret_sharing/polynomial"
	pb "github.com/lightsparkdev/spark/proto/spark_internal"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/lightsparkdev/spark/so/helper"
)

// TODO: signcrypt or something from the sending SO (in h.config) to the recipient SO (specified in the message)

type FixKeyshareArgs struct {
	badKeyshare   *ent.SigningKeyshare
	badOperator   *so.SigningOperator
	goodOperators map[string]*so.SigningOperator
}

type FixKeyshareHandler struct {
	config *so.Config
}

func NewFixKeyshareHandler(soConfig *so.Config) FixKeyshareHandler {
	return FixKeyshareHandler{
		config: soConfig,
	}
}

func (h *FixKeyshareHandler) parseRequest(ctx context.Context, badKeyshareId string, badOperatorIdentifier string, goodOperatorIdentifiers []string) (*FixKeyshareArgs, error) {
	// Parse the bad operator.
	badOperator, ok := h.config.SigningOperatorMap[badOperatorIdentifier]
	if !ok {
		return nil, fmt.Errorf("bad signing operator ID is not a known signing operator: %s", badOperatorIdentifier)
	}

	// Make sure there are the right number of good operators.
	if uint64(len(goodOperatorIdentifiers)) != h.config.Threshold {
		return nil, fmt.Errorf("number of good signing operators is not the threshold: need %d, have %d", h.config.Threshold, len(goodOperatorIdentifiers))
	}

	// Parse all the good operators.
	goodOperators := make(map[string]*so.SigningOperator)
	for _, identifier := range goodOperatorIdentifiers {
		operator, ok := h.config.SigningOperatorMap[identifier]
		if !ok {
			return nil, fmt.Errorf("good signing operator ID is not a known signing operator: %s", identifier)
		}

		goodOperators[identifier] = operator
	}

	// Parse the bad keyshare.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return nil, err
	}

	badKeyshareUUID, err := uuid.Parse(badKeyshareId)
	if err != nil {
		return nil, fmt.Errorf("failed to parse bad keyshare ID: %w", err)
	}

	badKeyshare, err := db.SigningKeyshare.Get(ctx, badKeyshareUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get bad keyshare: %w", err)
	}

	args := FixKeyshareArgs{
		badKeyshare:   badKeyshare,
		badOperator:   badOperator,
		goodOperators: goodOperators,
	}

	return &args, nil
}

// FixKeyshare fixes the bad key share using the good key shares.
// The operator with the bad key share acts as coordinator.
func (h FixKeyshareHandler) FixKeyshare(ctx context.Context, req *pb.FixKeyshareRequest) error {
	args, err := h.parseRequest(ctx, req.BadKeyshareId, req.BadOperatorId, req.GoodOperatorIds)
	if err != nil {
		return fmt.Errorf("fix keyshare error: %w", err)
	}

	// Make sure the coordinator (the current signing operator) is the request's bad operator.
	if h.config.Identifier != args.badOperator.Identifier {
		return fmt.Errorf("fix keyshare error: coordinator must be the bad operator: coordinator ID should be %s, is %s", args.badOperator.Identifier, h.config.Identifier)
	}

	goodOperatorIdentifiers := slices.Collect(maps.Keys(args.goodOperators))
	senders, err := helper.NewPreSelectedOperatorSelection(h.config, goodOperatorIdentifiers)
	if err != nil {
		return fmt.Errorf("fix keyshare error: %w", err)
	}

	// === Round 1 ===

	responses1, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, senders, func(ctx context.Context, operator *so.SigningOperator) (*pb.FixKeyshareRound1Response, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		round1Request := pb.FixKeyshareRound1Request{
			BadKeyshareId:   req.BadKeyshareId,
			BadOperatorId:   req.BadOperatorId,
			GoodOperatorIds: req.GoodOperatorIds,
		}

		client := pb.NewSparkInternalServiceClient(conn)
		return client.FixKeyshareRound1(ctx, &round1Request)
	})
	if err != nil {
		return fmt.Errorf("fix keyshare error: %w", err)
	}

	// Route messages from round 1 to round 2

	messages1To := make(map[secretsharing.PartyIndex][][]byte)

	for _, response := range responses1 {
		for _, msgBytes := range response.Message {
			// Temporarily deserialize the message to find its intended recipient.
			msg := secretsharing.Message[secretsharing.IssuePayload1]{}
			err := json.Unmarshal(msgBytes, &msg)
			if err != nil {
				return fmt.Errorf("fix keyshare error: %w", err)
			}

			_, present := messages1To[msg.To]
			if !present {
				messages1To[msg.To] = make([][]byte, 0)
			}
			messages1To[msg.To] = append(messages1To[msg.To], msgBytes)
		}
	}

	// === Round 2 ===

	responses2, err := helper.ExecuteTaskWithAllOperators(ctx, h.config, senders, func(ctx context.Context, operator *so.SigningOperator) (*pb.FixKeyshareRound2Response, error) {
		conn, err := operator.NewOperatorGRPCConnection()
		if err != nil {
			return nil, err
		}
		defer conn.Close()

		round2Request := pb.FixKeyshareRound2Request{
			BadKeyshareId:   req.BadKeyshareId,
			BadOperatorId:   req.BadOperatorId,
			GoodOperatorIds: req.GoodOperatorIds,
			Message:         messages1To[operator.Identifier],
		}

		client := pb.NewSparkInternalServiceClient(conn)
		return client.FixKeyshareRound2(ctx, &round2Request)
	})
	if err != nil {
		return fmt.Errorf("fix keyshare error: %w", err)
	}

	// Route messages from round 2 to round 3

	messages2 := make([][]byte, 0)

	for _, response := range responses2 {
		messages2 = append(messages2, response.Message)
	}

	// === Round 3 ===

	// Since the coordinator is the operator with the bad key share,
	// we can call round 3 directly instead of by RPC.
	outPayload3, err := h.coreRound3(args, messages2)
	// err = h.Round3(ctx, args, messages2)
	if err != nil {
		return fmt.Errorf("fix keyshare error: %w", err)
	}

	err = h.updateWithFixed(ctx, outPayload3, args.badKeyshare)
	if err != nil {
		return err
	}

	return nil
}

func (h FixKeyshareHandler) createConfig(args FixKeyshareArgs) (*secretsharing.IssueConfig, error) {
	request := secretsharing.IssueRequest{
		IssueIndex: args.badOperator.Identifier,
		BigI:       slices.Collect(maps.Keys(args.goodOperators)),
	}

	alphas := make(map[secretsharing.PartyIndex]*curve.Scalar)
	for identifier, operator := range args.goodOperators {
		// TODO: Don't hardcode the magic (+ 1) mapping
		// TODO: Somehow avoid unsafe cast
		alpha := curve.ScalarFromInt(uint32(operator.ID) + 1)

		alphas[identifier] = &alpha
	}

	// TODO: Don't hardcode the magic (+ 1) mapping
	// TODO: Somehow avoid unsafe cast
	badAlpha := curve.ScalarFromInt(uint32(args.badOperator.ID) + 1)
	alphas[args.badOperator.Identifier] = &badAlpha

	config := secretsharing.IssueConfig{
		IssueRequest: request,
		Sid:          []byte("unused"),
		T:            int(h.config.Threshold), // TODO: Somehow avoid unsafe cast
		Alphas:       alphas,
	}

	return &config, nil
}

func (h FixKeyshareHandler) createSender(args FixKeyshareArgs) (*secretsharing.IssueSender, error) {
	config, err := h.createConfig(args)
	if err != nil {
		return nil, err
	}

	sb := args.badKeyshare.SecretShare
	ownSecretShare, err := curve.ParseScalar(sb)
	if err != nil {
		return nil, fmt.Errorf("failed to parse own secret share: %w", err)
	}

	pubShareEvals := make([]polynomial.PointEval, 0)

	for goodIdentifier, goodOperator := range args.goodOperators {
		publicShareCompressed := args.badKeyshare.PublicShares[goodIdentifier]

		sharePubKey, err := keys.ParsePublicKey(publicShareCompressed)
		if err != nil {
			return nil, fmt.Errorf("failed to parse public key share for operator %s: %w", goodIdentifier, err)
		}

		sharePoint := curve.NewPointFromPublicKey(sharePubKey)

		eval := polynomial.PointEval{
			// TODO: Don't hardcode the magic (+ 1) mapping
			// TODO: Somehow avoid unsafe cast
			X: curve.ScalarFromInt(uint32(goodOperator.ID) + 1),
			Y: sharePoint,
		}

		pubShareEvals = append(pubShareEvals, eval)
	}

	pubSharesPoly := polynomial.NewInterpolatingPointPolynomial(pubShareEvals)

	sender, err := secretsharing.NewIssueSender(*config, h.config.Identifier, &ownSecretShare, pubSharesPoly)
	if err != nil {
		return nil, fmt.Errorf("failed to create sender: %w", err)
	}

	sender.AssumeAllPartiesHaveSameCoefficients()

	return sender, nil
}

func (h FixKeyshareHandler) createReceiver(args FixKeyshareArgs) (*secretsharing.IssueReceiver, error) {
	config, err := h.createConfig(args)
	if err != nil {
		return nil, err
	}

	receiver := secretsharing.IssueReceiver{
		Config: *config,
	}

	return &receiver, nil
}

func (h FixKeyshareHandler) Round1(ctx context.Context, req *pb.FixKeyshareRound1Request) (*pb.FixKeyshareRound1Response, error) {
	args, err := h.parseRequest(ctx, req.BadKeyshareId, req.BadOperatorId, req.GoodOperatorIds)
	if err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	return h.coreRound1(args)
}

func (h FixKeyshareHandler) coreRound1(args *FixKeyshareArgs) (*pb.FixKeyshareRound1Response, error) {
	// Make sure this operator is one of the request's good operators.
	_, exists := args.goodOperators[h.config.Identifier]
	if !exists {
		return nil, fmt.Errorf("a sender must be a good operator: sender ID is %s, should be in %v", h.config.Identifier, slices.Collect(maps.Keys(args.goodOperators)))
	}

	sender, err := h.createSender(*args)
	if err != nil {
		return nil, err
	}

	// Run the local protocol round.
	outMessages, err := sender.Round1()
	if err != nil {
		return nil, err
	}

	// Serialize outbound messages for transport.
	var outMessageEncodings [][]byte
	for _, msg := range outMessages {
		encoding, err := json.Marshal(msg)
		if err != nil {
			return nil, err
		}
		outMessageEncodings = append(outMessageEncodings, encoding)
	}

	response := pb.FixKeyshareRound1Response{
		Message: outMessageEncodings,
	}

	return &response, nil
}

func (h FixKeyshareHandler) Round2(ctx context.Context, req *pb.FixKeyshareRound2Request) (*pb.FixKeyshareRound2Response, error) {
	args, err := h.parseRequest(ctx, req.BadKeyshareId, req.BadOperatorId, req.GoodOperatorIds)
	if err != nil {
		return nil, fmt.Errorf("failed to parse request: %w", err)
	}

	return h.coreRound2(args, req.Message)
}

func (h FixKeyshareHandler) coreRound2(args *FixKeyshareArgs, messages [][]byte) (*pb.FixKeyshareRound2Response, error) {
	// Make sure this operator is one of the request's good operators.
	_, exists := args.goodOperators[h.config.Identifier]
	if !exists {
		return nil, fmt.Errorf("a sender must be a good operator: sender ID is %s, should be in %v", h.config.Identifier, slices.Collect(maps.Keys(args.goodOperators)))
	}

	// In principle, we could cache the sender, but reconstructing it is simpler.
	sender, err := h.createSender(*args)
	if err != nil {
		return nil, err
	}

	// Deserialize inbound messages from transport.
	payloadFrom := make(map[secretsharing.PartyIndex]secretsharing.IssuePayload1)
	for _, msgBytes := range messages {
		msg := secretsharing.Message[secretsharing.IssuePayload1]{}
		err := json.Unmarshal(msgBytes, &msg)
		if err != nil {
			return nil, err
		}

		_, alreadySet := payloadFrom[msg.From]
		if alreadySet {
			return nil, fmt.Errorf("more than one message from party %s to party %s", msg.From, msg.To)
		}
		payloadFrom[msg.From] = msg.Payload
	}

	// Run the local protocol round.
	outMessage, err := sender.Round2(payloadFrom)
	if err != nil {
		return nil, err
	}

	// Serialize outbound message for transport.
	outMessageEncoding, err := json.Marshal(outMessage)
	if err != nil {
		return nil, err
	}

	response := pb.FixKeyshareRound2Response{
		Message: outMessageEncoding,
	}

	return &response, nil
}

func (h FixKeyshareHandler) coreRound3(args *FixKeyshareArgs, messages [][]byte) (*secretsharing.IssuePayload3, error) {
	// Make sure this operator is the request's bad operator.
	if h.config.Identifier != args.badOperator.Identifier {
		return nil, fmt.Errorf("the receiver must be the bad operator: receiver ID is %s, should be %s", h.config.Identifier, args.badOperator.Identifier)
	}

	receiver, err := h.createReceiver(*args)
	if err != nil {
		return nil, err
	}

	// Deserialize inbound messages from transport.
	payloadFrom := make(map[secretsharing.PartyIndex]secretsharing.IssuePayload2)
	for _, msgBytes := range messages {
		msg := secretsharing.Message[secretsharing.IssuePayload2]{}
		err := json.Unmarshal(msgBytes, &msg)
		if err != nil {
			return nil, err
		}

		_, alreadySet := payloadFrom[msg.From]
		if alreadySet {
			return nil, fmt.Errorf("more than one message from party %s to party %s", msg.From, msg.To)
		}
		payloadFrom[msg.From] = msg.Payload
	}

	// Run the local protocol round.
	outPayload, err := receiver.Round3(payloadFrom)
	if err != nil {
		return nil, err
	}

	return outPayload, nil
}

func (h FixKeyshareHandler) updateWithFixed(ctx context.Context, outPayload *secretsharing.IssuePayload3, badKeyshare *ent.SigningKeyshare) error {
	// Recover the public sharing polynomial.
	pubSharesPoly := outPayload.MathcalB.Decode()

	// Recover the public shares.
	pubShares := make(map[string]keys.Public)
	for identifier, operator := range h.config.SigningOperatorMap {
		// TODO: Don't hardcode the magic (+ 1) mapping
		// TODO: Somehow avoid unsafe cast
		alpha := curve.ScalarFromInt(uint32(operator.ID) + 1)

		pubShare, err := pubSharesPoly.Eval(alpha).ToPublicKey()
		if err != nil {
			return fmt.Errorf("invalid public share: %w", err)
		}

		pubShares[identifier] = pubShare
	}

	// Recover the public key.
	pubKeyPoint := pubSharesPoly.Eval(curve.ScalarFromInt(0))
	pubKey, err := pubKeyPoint.ToPublicKey()
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	// Update to fix the bad keyshare in the database.
	db, err := ent.GetDbFromContext(ctx)
	if err != nil {
		return err
	}

	_, err = db.SigningKeyshare.UpdateOneID(badKeyshare.ID).
		SetSecretShare(outPayload.SIssue.Serialize()).
		SetPublicShares(keys.ToBytesMap(pubShares)).
		SetPublicKey(pubKey.Serialize()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to update keyshare: %w", err)
	}

	return nil
}
