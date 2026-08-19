package tokens

import (
	"context"

	"github.com/lightsparkdev/spark/common"
	pbgossip "github.com/lightsparkdev/spark/proto/gossip"
	tokenpb "github.com/lightsparkdev/spark/proto/spark_token"
	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/ent"
)

// revokeAllowanceGossipMessage wraps an owner-signed revocation into a gossip
// message carrying exactly the fields of the signed RevokeTokenAllowancePayload
// plus the signature, so each receiving operator re-verifies it independently.
func revokeAllowanceGossipMessage(revokePayload *tokenpb.RevokeTokenAllowancePayload, ownerSignature []byte) *pbgossip.GossipMessage {
	return &pbgossip.GossipMessage{
		Message: &pbgossip.GossipMessage_RevokeTokenAllowance{
			RevokeTokenAllowance: &pbgossip.GossipMessageRevokeTokenAllowance{
				AllowanceId:            revokePayload.GetAllowanceId(),
				OwnerPublicKey:         revokePayload.GetOwnerPublicKey(),
				RevokeVersion:          revokePayload.GetVersion(),
				OwnerProvidedTimestamp: revokePayload.GetOwnerProvidedTimestamp(),
				OwnerSignature:         ownerSignature,
			},
		},
	}
}

// buildAllowanceRevokeProgress reports which operators have applied the revoke
// as of the immediate best-effort send: self (applied inline) plus every peer
// whose receipt bit is set in the returned Gossip row. Peers not yet acked are
// converged by the send_gossip retry task, so this snapshot is advisory.
func buildAllowanceRevokeProgress(config *so.Config, participants []string, gossipRow *ent.Gossip) *tokenpb.AllowanceProgress {
	applied := [][]byte{config.IdentityPublicKey().Serialize()}
	if gossipRow != nil && gossipRow.Receipts != nil {
		bitMap := common.NewBitMapFromBytes(*gossipRow.Receipts, len(participants))
		for i, id := range participants {
			if !bitMap.Get(i) {
				continue
			}
			if operator, ok := config.SigningOperatorMap[id]; ok {
				applied = append(applied, operator.IdentityPublicKey.Serialize())
			}
		}
	}
	return &tokenpb.AllowanceProgress{AppliedOperatorPublicKeys: applied}
}

// ApplyRevokeTokenAllowanceGossip applies a gossip-delivered token-allowance
// revocation on this operator. It reconstructs the owner-signed revoke payload
// from the message and re-runs ValidateAndApplyRevokeAllowance, which
// re-verifies the owner signature against the key recorded on the grant - the
// same independent validation every operator performs. Deliberately not gated
// on the allowances-enable knob: revocation is a security control that must keep
// converging while the feature is disabled.
func ApplyRevokeTokenAllowanceGossip(ctx context.Context, config *so.Config, msg *pbgossip.GossipMessageRevokeTokenAllowance) error {
	revokePayload := &tokenpb.RevokeTokenAllowancePayload{
		Version:                msg.GetRevokeVersion(),
		AllowanceId:            msg.GetAllowanceId(),
		OwnerPublicKey:         msg.GetOwnerPublicKey(),
		OwnerProvidedTimestamp: msg.GetOwnerProvidedTimestamp(),
	}
	return ValidateAndApplyRevokeAllowance(ctx, config, revokePayload, msg.GetOwnerSignature())
}
