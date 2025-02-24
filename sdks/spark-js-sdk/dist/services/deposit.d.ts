import { Transaction } from "@scure/btc-signer";
import { FinalizeNodeSignaturesResponse, GenerateDepositAddressResponse } from "../proto/spark.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
export type GenerateDepositAddressParams = {
    signingPubkey: Uint8Array;
};
export type CreateTreeRootParams = {
    signingPubKey: Uint8Array;
    verifyingKey: Uint8Array;
    depositTx: Transaction;
    vout: number;
};
export declare class DepositService {
    private readonly config;
    private readonly connectionManager;
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    private validateDepositAddress;
    generateDepositAddress({ signingPubkey, }: GenerateDepositAddressParams): Promise<GenerateDepositAddressResponse>;
    createTreeRoot({ signingPubKey, verifyingKey, depositTx, vout, }: CreateTreeRootParams): Promise<FinalizeNodeSignaturesResponse>;
}
