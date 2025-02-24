// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import BitcoinNetwork from './BitcoinNetwork.js';
export const RequestLightningReceiveInputFromJson = (obj) => {
    return {
        network: BitcoinNetwork[obj["request_lightning_receive_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        amountSats: obj["request_lightning_receive_input_amount_sats"],
        paymentHash: obj["request_lightning_receive_input_payment_hash"],
        expirySecs: obj["request_lightning_receive_input_expiry_secs"],
        memo: obj["request_lightning_receive_input_memo"],
    };
};
export const RequestLightningReceiveInputToJson = (obj) => {
    return {
        request_lightning_receive_input_network: obj.network,
        request_lightning_receive_input_amount_sats: obj.amountSats,
        request_lightning_receive_input_payment_hash: obj.paymentHash,
        request_lightning_receive_input_expiry_secs: obj.expirySecs,
        request_lightning_receive_input_memo: obj.memo,
    };
};
//# sourceMappingURL=RequestLightningReceiveInput.js.map