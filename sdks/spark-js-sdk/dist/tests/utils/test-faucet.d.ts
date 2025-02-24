import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
export type FaucetCoin = {
    key: Uint8Array;
    outpoint: TransactionInput;
    txout: TransactionOutput;
};
export declare class BitcoinFaucet {
    private url;
    private username;
    private password;
    private coins;
    private static instance;
    constructor(url: string, username: string, password: string);
    fund(): Promise<FaucetCoin>;
    refill(): Promise<void>;
    signFaucetCoin(unsignedTx: Transaction, fundingTxOut: TransactionOutput, key: Uint8Array): Promise<Transaction>;
    private call;
    generateToAddress(numBlocks: number, address: string): Promise<any>;
    getBlock(blockHash: string): Promise<any>;
    broadcastTx(txHex: string): Promise<any>;
}
