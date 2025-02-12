
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum LightningReceiveRequestStatus { 
/**
 * This is an enum value that represents values that could be added in the future.
 * Clients should support unknown values as more of them could be added without notice.
 */
 FUTURE_VALUE = "FUTURE_VALUE",

INVOICE_CREATED = "INVOICE_CREATED",

PAYMENT_PREIMAGE_REQUEST_RECEIVED = "PAYMENT_PREIMAGE_REQUEST_RECEIVED",

LEAVES_LOCKED = "LEAVES_LOCKED",

REFUND_SIGNING_COMMITMENTS_RECEIVED = "REFUND_SIGNING_COMMITMENTS_RECEIVED",

REFUND_SIGNED = "REFUND_SIGNED",

PAYMENT_PREIMAGE_RECOVERED = "PAYMENT_PREIMAGE_RECOVERED",

LIGHTNING_PAYMENT_RECEIVED = "LIGHTNING_PAYMENT_RECEIVED",

TRANSFER_COMPLETED = "TRANSFER_COMPLETED",

}

export default LightningReceiveRequestStatus;
