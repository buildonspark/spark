package entcomments

import "slices"

// legacyUncommentedFields tracks fields that existed before comment enforcement was added.
// These fields are grandfathered in and exempt from the comment requirement.
//
// New fields MUST have comments - this allowlist should only shrink over time as we
// add documentation to legacy fields.
//
// To remove a field from this list:
// 1. Add a .Comment("...") to the field definition in the schema
// 2. Remove the field from this map
// 3. Run `make ent` to verify it still generates successfully
var legacyUncommentedFields = map[string][]string{
	"EntityDkgKey":                      {"key_type"},
	"Gossip":                            {"participants", "message", "receipts", "status"},
	"L1TokenCreate":                     {"transaction_id", "issuer_public_key", "token_name", "token_ticker", "decimals", "max_supply", "is_freezable", "network", "token_identifier"},
	"PaymentIntent":                     {"payment_intent"},
	"PendingSendTransfer":               {"transfer_id", "status"},
	"PreimageRequest":                   {"payment_hash", "status", "receiver_identity_pubkey", "preimage", "sender_identity_pubkey"},
	"PreimageShare":                     {"payment_hash", "preimage_share", "threshold", "owner_identity_pubkey", "invoice_string"},
	"SigningCommitment":                 {"operator_index", "status", "nonce_commitment"},
	"SigningNonce":                      {"nonce", "nonce_commitment", "message", "retry_fingerprint"},
	"TokenCreate":                       {"issuer_signature", "operator_specific_issuer_signature", "creation_entity_public_key", "wallet_provided_timestamp", "issuer_public_key", "token_name", "token_ticker", "decimals", "max_supply", "is_freezable", "network", "token_identifier"},
	"TokenFreeze":                       {"status", "owner_public_key", "token_public_key", "issuer_signature", "wallet_provided_freeze_timestamp", "wallet_provided_thaw_timestamp", "token_create_id"},
	"TokenMint":                         {"issuer_public_key", "wallet_provided_timestamp", "issuer_signature", "operator_specific_issuer_signature", "token_identifier"},
	"TokenOutput":                       {"status", "owner_public_key", "withdraw_bond_sats", "withdraw_relative_block_locktime", "withdraw_revocation_commitment", "token_public_key", "token_amount", "created_transaction_output_vout", "spent_ownership_signature", "spent_operator_specific_ownership_signature", "spent_transaction_input_vout", "spent_revocation_secret", "confirmed_withdraw_block_hash", "network", "token_identifier", "token_create_id"},
	"TokenPartialRevocationSecretShare": {"operator_identity_public_key", "secret_share"},
	"TokenTransaction":                  {"partial_token_transaction_hash", "finalized_token_transaction_hash", "operator_signature", "status", "expiry_time", "coordinator_public_key", "client_created_timestamp", "version", "validity_duration_seconds"},
	"TokenTransactionPeerSignature":     {"operator_identity_public_key", "signature"},
	"TransferLeaf":                      {"secret_cipher", "signature", "previous_refund_tx", "previous_direct_refund_tx", "previous_direct_from_cpfp_refund_tx", "intermediate_refund_tx", "intermediate_direct_refund_tx", "intermediate_direct_from_cpfp_refund_tx", "key_tweak", "sender_key_tweak_proof", "receiver_key_tweak"},
	"Tree":                              {"owner_identity_pubkey", "status", "network", "base_txid", "vout"},
	"UserSignedTransaction":             {"transaction", "user_signature", "signing_commitments", "user_signature_commitment"},
	"Utxo":                              {"block_height", "txid", "vout", "amount", "network", "pk_script"},
	"UtxoSwap":                          {"status", "request_type", "credit_amount_sats", "max_fee_sats", "ssp_signature", "ssp_identity_public_key", "user_signature", "user_identity_public_key", "coordinator_identity_public_key", "requested_transfer_id", "spend_tx_signing_result"},
}

// isLegacyUncommentedField returns true if the given schema and field are grandfathered
// in from before comment enforcement was added.
func isLegacyUncommentedField(schemaName, fieldName string) bool {
	if allowedFields, ok := legacyUncommentedFields[schemaName]; ok {
		return slices.Contains(allowedFields, fieldName)
	}
	return false
}
