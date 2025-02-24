import { TreeNode } from "../proto/spark.js";
import { SigningOperator, WalletConfig } from "../services/config.js";
import { SparkWallet } from "../spark-sdk.js";
import { Network } from "../utils/network.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";
export declare const LOCAL_WALLET_CONFIG: {
    network: Network;
    coodinatorIdentifier: string;
    frostSignerAddress: string;
    threshold: number;
    signingOperators: Record<string, SigningOperator>;
};
export declare const REGTEST_WALLET_CONFIG: {
    network: Network;
    coodinatorIdentifier: string;
    frostSignerAddress: string;
    threshold: number;
    signingOperators: Record<string, SigningOperator>;
};
export declare function getRegtestSigningOperators(): Record<string, SigningOperator>;
export declare function getLocalSigningOperators(): Record<string, SigningOperator>;
export declare function getTestWalletConfig(): WalletConfig;
export declare function getTestWalletConfigWithIdentityKey(identityPrivateKey: Uint8Array): WalletConfig;
export declare function createNewTree(wallet: SparkWallet, pubKey: Uint8Array, faucet: BitcoinFaucet, amountSats?: bigint): Promise<TreeNode>;
