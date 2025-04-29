
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum SparkUserRequestType { 
/**
 * This is an enum value that represents values that could be added in the future.
 * Clients should support unknown values as more of them could be added without notice.
 */
 FUTURE_VALUE = "FUTURE_VALUE",

LIGHTNING_SEND = "LIGHTNING_SEND",

LIGHTNING_RECEIVE = "LIGHTNING_RECEIVE",

COOP_EXIT = "COOP_EXIT",

LEAVES_SWAP = "LEAVES_SWAP",

}

export default SparkUserRequestType;
