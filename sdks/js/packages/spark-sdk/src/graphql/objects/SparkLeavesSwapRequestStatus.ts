
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum SparkLeavesSwapRequestStatus { 
/**
 * This is an enum value that represents values that could be added in the future.
 * Clients should support unknown values as more of them could be added without notice.
 */
 FUTURE_VALUE = "FUTURE_VALUE",

CREATED = "CREATED",

INITIATED = "INITIATED",

LEAVES_LOCKED = "LEAVES_LOCKED",

REFUND_TX_ADAPTOR_SIGNED = "REFUND_TX_ADAPTOR_SIGNED",

INBOUND_TRANSFER_CLAIMED = "INBOUND_TRANSFER_CLAIMED",

SUCCEEDED = "SUCCEEDED",

EXPIRED = "EXPIRED",

FAILED = "FAILED",

}

export default SparkLeavesSwapRequestStatus;
