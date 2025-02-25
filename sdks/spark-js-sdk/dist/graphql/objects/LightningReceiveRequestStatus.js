// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export var LightningReceiveRequestStatus;
(function (LightningReceiveRequestStatus) {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    LightningReceiveRequestStatus["FUTURE_VALUE"] = "FUTURE_VALUE";
    LightningReceiveRequestStatus["INVOICE_CREATED"] = "INVOICE_CREATED";
    LightningReceiveRequestStatus["TRANSFER_CREATED"] = "TRANSFER_CREATED";
    LightningReceiveRequestStatus["TRANSFER_CREATION_FAILED"] = "TRANSFER_CREATION_FAILED";
    LightningReceiveRequestStatus["REFUND_SIGNING_COMMITMENTS_QUERYING_FAILED"] = "REFUND_SIGNING_COMMITMENTS_QUERYING_FAILED";
    LightningReceiveRequestStatus["REFUND_SIGNING_FAILED"] = "REFUND_SIGNING_FAILED";
    LightningReceiveRequestStatus["PAYMENT_PREIMAGE_RECOVERED"] = "PAYMENT_PREIMAGE_RECOVERED";
    LightningReceiveRequestStatus["PAYMENT_PREIMAGE_RECOVERING_FAILED"] = "PAYMENT_PREIMAGE_RECOVERING_FAILED";
    LightningReceiveRequestStatus["LIGHTNING_PAYMENT_RECEIVED"] = "LIGHTNING_PAYMENT_RECEIVED";
    LightningReceiveRequestStatus["TRANSFER_FAILED"] = "TRANSFER_FAILED";
    LightningReceiveRequestStatus["TRANSFER_COMPLETED"] = "TRANSFER_COMPLETED";
})(LightningReceiveRequestStatus || (LightningReceiveRequestStatus = {}));
export default LightningReceiveRequestStatus;
//# sourceMappingURL=LightningReceiveRequestStatus.js.map