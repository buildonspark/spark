
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


export enum LightningSendRequestStatus {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    FUTURE_VALUE = "FUTURE_VALUE",

    CREATED = "CREATED",

    USER_TRANSFER_VALIDATION_FAILED = "USER_TRANSFER_VALIDATION_FAILED",

    LIGHTNING_PAYMENT_INITIATED = "LIGHTNING_PAYMENT_INITIATED",

    LIGHTNING_PAYMENT_FAILED = "LIGHTNING_PAYMENT_FAILED",

    LIGHTNING_PAYMENT_SUCCEEDED = "LIGHTNING_PAYMENT_SUCCEEDED",

    PREIMAGE_PROVIDED = "PREIMAGE_PROVIDED",

    PREIMAGE_PROVIDING_FAILED = "PREIMAGE_PROVIDING_FAILED",

    TRANSFER_COMPLETED = "TRANSFER_COMPLETED",

    TRANSFER_FAILED = "TRANSFER_FAILED",

    PENDING_USER_SWAP_RETURN = "PENDING_USER_SWAP_RETURN",

    USER_SWAP_RETURNED = "USER_SWAP_RETURNED",

    USER_SWAP_RETURN_FAILED = "USER_SWAP_RETURN_FAILED",

    REQUEST_VALIDATED = "REQUEST_VALIDATED",

}

export default LightningSendRequestStatus;
