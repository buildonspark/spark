// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
export var LightningReceiveRequestStatus;
(function (LightningReceiveRequestStatus) {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    LightningReceiveRequestStatus["FUTURE_VALUE"] = "FUTURE_VALUE";
    LightningReceiveRequestStatus["INVOICE_CREATED"] = "INVOICE_CREATED";
    LightningReceiveRequestStatus["PAYMENT_PREIMAGE_REQUEST_RECEIVED"] = "PAYMENT_PREIMAGE_REQUEST_RECEIVED";
    LightningReceiveRequestStatus["LEAVES_LOCKED"] = "LEAVES_LOCKED";
    LightningReceiveRequestStatus["REFUND_SIGNING_COMMITMENTS_RECEIVED"] = "REFUND_SIGNING_COMMITMENTS_RECEIVED";
    LightningReceiveRequestStatus["REFUND_SIGNED"] = "REFUND_SIGNED";
    LightningReceiveRequestStatus["PAYMENT_PREIMAGE_RECOVERED"] = "PAYMENT_PREIMAGE_RECOVERED";
    LightningReceiveRequestStatus["LIGHTNING_PAYMENT_RECEIVED"] = "LIGHTNING_PAYMENT_RECEIVED";
    LightningReceiveRequestStatus["TRANSFER_COMPLETED"] = "TRANSFER_COMPLETED";
})(LightningReceiveRequestStatus || (LightningReceiveRequestStatus = {}));
export default LightningReceiveRequestStatus;
//# sourceMappingURL=LightningReceiveRequestStatus.js.map