import { TransactionInput } from "@scure/btc-signer/psbt";
import { Transfer } from "../proto/spark.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection.js";
import { BaseTransferService, LeafKeyTweak } from "./transfer.js";
export type GetConnectorRefundSignaturesParams = {
    leaves: LeafKeyTweak[];
    exitTxId: Uint8Array;
    connectorOutputs: TransactionInput[];
    receiverPubKey: Uint8Array;
};
export declare class CoopExitService extends BaseTransferService {
    constructor(config: WalletConfigService, connectionManager: ConnectionManager);
    getConnectorRefundSignatures({ leaves, exitTxId, connectorOutputs, receiverPubKey, }: GetConnectorRefundSignaturesParams): Promise<{
        transfer: Transfer;
        signaturesMap: Map<string, Uint8Array>;
    }>;
    private createConnectorRefundTransaction;
    private signCoopExitRefunds;
}
