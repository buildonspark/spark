
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum SparkCoopExitRequestStatus { 
/**
 * This is an enum value that represents values that could be added in the future.
 * Clients should support unknown values as more of them could be added without notice.
 */
 FUTURE_VALUE = "FUTURE_VALUE",

INITIATED = "INITIATED",

TX_SIGNED = "TX_SIGNED",

TX_BROADCASTED = "TX_BROADCASTED",

WAITING_ON_TX_CONFIRMATIONS = "WAITING_ON_TX_CONFIRMATIONS",

SUCCEEDED = "SUCCEEDED",

EXPIRED = "EXPIRED",

FAILED = "FAILED",

}

export default SparkCoopExitRequestStatus;
