import { SparkSigner } from "../signer/signer.js";
import { Network } from "../utils/network.js";
export type SigningOperator = {
    id: number;
    identifier: string;
    address: string;
    identityPublicKey: Uint8Array;
};
export type WalletConfig = {
    network: Network;
    signingOperators: Record<string, SigningOperator>;
    coodinatorIdentifier: string;
    frostSignerAddress: string;
    threshold: number;
};
export declare class WalletConfigService {
    private config;
    readonly signer: SparkSigner;
    constructor(network: Network, signer?: SparkSigner);
    getCoordinatorAddress(): string;
    getConfig(): WalletConfig;
    getNetwork(): Network;
    getNetworkProto(): import("../proto/spark.js").Network;
}
