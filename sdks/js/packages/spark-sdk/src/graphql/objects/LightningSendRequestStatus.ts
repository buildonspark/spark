
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum LightningSendRequestStatus { 
/**
 * This is an enum value that represents values that could be added in the future.
 * Clients should support unknown values as more of them could be added without notice.
 */
 FUTURE_VALUE = "FUTURE_VALUE",

CREATED = "CREATED",

REQUEST_VALIDATED = "REQUEST_VALIDATED",

LIGHTNING_PAYMENT_INITIATED = "LIGHTNING_PAYMENT_INITIATED",

LIGHTNING_PAYMENT_FAILED = "LIGHTNING_PAYMENT_FAILED",

LIGHTNING_PAYMENT_SUCCEEDED = "LIGHTNING_PAYMENT_SUCCEEDED",

PREIMAGE_PROVIDED = "PREIMAGE_PROVIDED",

TRANSFER_COMPLETED = "TRANSFER_COMPLETED",

}

export default LightningSendRequestStatus;
