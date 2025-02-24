// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
import BitcoinNetwork from './BitcoinNetwork.js';
export const LightningReceiveFeeEstimateInputFromJson = (obj) => {
    return {
        network: BitcoinNetwork[obj["lightning_receive_fee_estimate_input_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        amountSats: obj["lightning_receive_fee_estimate_input_amount_sats"],
    };
};
export const LightningReceiveFeeEstimateInputToJson = (obj) => {
    return {
        lightning_receive_fee_estimate_input_network: obj.network,
        lightning_receive_fee_estimate_input_amount_sats: obj.amountSats,
    };
};
//# sourceMappingURL=LightningReceiveFeeEstimateInput.js.map