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

const GetLightningReceiveFeeEstimateQuery = `
query GetLightningReceiveFeeEstimate(
  $network: BitcoinNetwork!
  $amount_sats: Long!
) {
  lightning_receive_fee_estimate(input: {
    network: $network
    amount_sats: $amount_sats
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
) {
  request_coop_exit(input: {
    leaf_external_ids: $leaf_external_ids
    withdrawal_address: $withdrawal_address
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
        encoded_envoice
      }
      fee {
        original_value
        original_unit
      }
    }
  }
}
`
