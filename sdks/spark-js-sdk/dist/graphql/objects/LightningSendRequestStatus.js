// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export var LightningSendRequestStatus;
(function (LightningSendRequestStatus) {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    LightningSendRequestStatus["FUTURE_VALUE"] = "FUTURE_VALUE";
    LightningSendRequestStatus["CREATED"] = "CREATED";
    LightningSendRequestStatus["REQUEST_VALIDATED"] = "REQUEST_VALIDATED";
    LightningSendRequestStatus["LIGHTNING_PAYMENT_INITIATED"] = "LIGHTNING_PAYMENT_INITIATED";
    LightningSendRequestStatus["LIGHTNING_PAYMENT_FAILED"] = "LIGHTNING_PAYMENT_FAILED";
    LightningSendRequestStatus["LIGHTNING_PAYMENT_SUCCEEDED"] = "LIGHTNING_PAYMENT_SUCCEEDED";
    LightningSendRequestStatus["PREIMAGE_PROVIDED"] = "PREIMAGE_PROVIDED";
    LightningSendRequestStatus["TRANSFER_COMPLETED"] = "TRANSFER_COMPLETED";
})(LightningSendRequestStatus || (LightningSendRequestStatus = {}));
export default LightningSendRequestStatus;
//# sourceMappingURL=LightningSendRequestStatus.js.map