import { QueryPendingTransfersResponse, Transfer } from "../../proto/spark.js";
import { SparkSigner } from "../../signer/signer.js";
import { SparkWallet } from "../../spark-sdk.js";
interface ISparkWalletTesting extends SparkWallet {
    getSigner(): SparkSigner;
    queryPendingTransfers(): Promise<QueryPendingTransfersResponse>;
    verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}
export declare class SparkWalletTesting extends SparkWallet implements ISparkWalletTesting {
    getSigner(): SparkSigner;
    queryPendingTransfers(): Promise<QueryPendingTransfersResponse>;
    verifyPendingTransfer(transfer: Transfer): Promise<Map<string, Uint8Array>>;
}
export {};
