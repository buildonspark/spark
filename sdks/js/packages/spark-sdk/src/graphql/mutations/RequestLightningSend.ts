import { FRAGMENT as RequestLightningSendOutputFragment } from "../objects/LightningSendRequest.js";
export const RequestLightningSend = `
  mutation RequestLightningSend(
    $encoded_invoice: String!
    $idempotency_key: String
    $amount_sats: Long
    $user_outbound_transfer_external_id: UUID
  ) {
    request_lightning_send(input: {
      encoded_invoice: $encoded_invoice
      idempotency_key: $idempotency_key
      amount_sats: $amount_sats
      user_outbound_transfer_external_id: $user_outbound_transfer_external_id
    }) {
      request {
        ...LightningSendRequestFragment
      }
    }
  }
  ${RequestLightningSendOutputFragment}
`;
