package sspapi

const GetCoopExitFeeEstimateQuery = `
query GetCoopExitFeeEstimate(
  $leaf_external_ids: [UUID!]!
  $withdrawal_address: String!
) {
  coop_exit_fee_estimate(input: {
    leaf_external_ids: $leaf_external_ids
    withdrawal_address: $withdrawal_address
  }) {
    fee_estimate {
      original_value
      original_unit
    }
  }
}
`

const GetLightningSendFeeEstimateQuery = `
query GetLightningSendFeeEstimate(
  $encoded_invoice: String!
) {
  lightning_send_fee_estimate(input: {
    encoded_invoice: $encoded_invoice
  }) {
    fee_estimate {
      original_value
      original_unit
    }
  }
}
`

const RequestCoopExitMutation = `
mutation RequestCoopExit(
  $leaf_external_ids: [UUID!]!
  $withdrawal_address: String!
  $idempotency_key: String!
) {
  request_coop_exit(input: {
    leaf_external_ids: $leaf_external_ids
    withdrawal_address: $withdrawal_address
    idempotency_key: $idempotency_key
  }) {
    request {
      id
      created_at
      updated_at
      fee {
        original_value
        original_unit
      }
      status
      raw_connector_transaction
      expires_at
    }
  }
}
`

const RequestLightningSendMutation = `
mutation RequestLightningSend(
  $encoded_invoice: String!
  $idempotency_key: String!
) {
  request_lightning_send(input: {
    encoded_invoice: $encoded_invoice
    idempotency_key: $idempotency_key
  }) {
    request {
      id
      created_at
      updated_at
      encoded_invoice
      fee {
        original_value
        original_unit
      }
	  status
    }
  }
}
`

const RequestLightningReceiveMutation = `
mutation RequestLightningReceive(
  $network: BitcoinNetwork!
  $amount_sats: Long!
  $payment_hash: Hash32!
  $expiry_secs: Int
  $memo: String
) {
  request_lightning_receive(input: {
    network: $network
    amount_sats: $amount_sats
    payment_hash: $payment_hash
    expiry_secs: $expiry_secs
    memo: $memo
  }) {
    request {
      id
      created_at
      updated_at
      invoice {
        encoded_invoice
      }
      fee {
        original_value
        original_unit
      }
    }
  }
}
`

const CompleteCoopExitMutation = `
mutation CompleteCoopExit(
  $user_outbound_transfer_external_id: UUID!
  $coop_exit_request_id: ID!
) {
  complete_coop_exit(input: {
    user_outbound_transfer_external_id: $user_outbound_transfer_external_id
    coop_exit_request_id: $coop_exit_request_id
  }) {
    request {
      id
    }
  }
}
`

const RequestLeavesSwapMutation = `
mutation RequestLeavesSwap(
  $adaptor_pubkey: String!
  $total_amount_sats: Long!
  $target_amount_sats: Long!
  $fee_sats: Long!
  $user_leaves: [UserLeafInput!]!
) {
  request_leaves_swap(input: {
    adaptor_pubkey: $adaptor_pubkey
    total_amount_sats: $total_amount_sats
    target_amount_sats: $target_amount_sats
    fee_sats: $fee_sats
    user_leaves: $user_leaves
  }) {
    request {
      id
      swap_leaves {
        leaf_id
        raw_unsigned_refund_transaction
        adaptor_signed_signature
      }
    }
  }
}
`

const CompleteLeavesSwapMutation = `
mutation CompleteLeavesSwap(
  $adaptor_secret_key: String!
  $user_outbound_transfer_external_id: UUID!
  $leaves_swap_request_id: ID!
) {
  complete_leaves_swap(input: {
    adaptor_secret_key: $adaptor_secret_key
    user_outbound_transfer_external_id: $user_outbound_transfer_external_id
    leaves_swap_request_id: $leaves_swap_request_id
  }) {
    request {
      id
    }
  }
}
`

const WalletUserIdentityPublicKeyMutation = `
mutation WalletUserIdentityPublicKey($phone_number: String!) {
  wallet_user_identity_public_key(input: {
    phone_number: $phone_number
  }) {
    identity_public_key
  }
}
`

const StartReleaseSeedMutation = `
mutation StartReleaseSeed($phone_number: String!) {
  start_seed_release(input: {
    phone_number: $phone_number
  })
}
`

const CompleteReleaseSeedMutation = `
mutation CompleteReleaseSeed($phone_number: String!, $code: String!) {
  complete_seed_release(input: {
    phone_number: $phone_number
    code: $code
  }) {
    seed
  }
}
`

const NotifyReceiverTransferMutation = `
mutation NotifyReceiverTransfer($phone_number: String!, $amount_sats: Long!) {
  notify_receiver_transfer(input: {
    phone_number: $phone_number
    amount_sats: $amount_sats
  })
}
`
