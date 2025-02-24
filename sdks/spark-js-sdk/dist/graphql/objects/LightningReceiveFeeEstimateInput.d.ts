import BitcoinNetwork from './BitcoinNetwork.js';
interface LightningReceiveFeeEstimateInput {
    network: BitcoinNetwork;
    amountSats: number;
}
export declare const LightningReceiveFeeEstimateInputFromJson: (obj: any) => LightningReceiveFeeEstimateInput;
export declare const LightningReceiveFeeEstimateInputToJson: (obj: LightningReceiveFeeEstimateInput) => any;
export default LightningReceiveFeeEstimateInput;
