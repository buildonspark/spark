import { IssuerSparkWallet } from "./services/spark/wallet.js";
// import * as bip39 from "@scure/bip39";
// import { HDKey } from "@scure/bip32";
// import { LRCWallet } from "lrc20-js-sdk";
// import { networks } from "bitcoinjs-lib";
// import { NetworkType } from "lrc20-js-sdk";
export class IssuerWallet {
    bitcoinWallet;
    sparkWallet;
    initialized = false;
    constructor(network) {
        this.sparkWallet = new IssuerSparkWallet(network);
    }
    async initWalletFromMnemonic(mnemonic, 
    // Set to true to enable L1 Token Announcements.
    enableL1Wallet = true) {
        await this.sparkWallet.initWalletFromMnemonic(mnemonic);
        if (enableL1Wallet) {
            // const seed = await bip39.mnemonicToSeed(mnemonic);
            // const hdkey = HDKey.fromMasterSeed(seed).derive("m/0").privateKey;
            // this.bitcoinWallet = createLRCWallet(
            //    bytesToHex.privateKey,
            //    networks.regtest,
            //    NetworkType.REGTEST);
        }
        this.initialized = true;
    }
    getSparkWallet() {
        if (!this.initialized || !this.sparkWallet) {
            throw new Error("Spark wallet not initialized");
        }
        return this.sparkWallet;
    }
    getBitcoinWallet() {
        if (!this.initialized || !this.bitcoinWallet) {
            throw new Error("Bitcoin wallet not initialized");
        }
        return this.sparkWallet !== undefined;
    }
    isSparkInitialized() {
        return this.initialized;
    }
    isL1Initialized() {
        return this.initialized && this.bitcoinWallet !== undefined;
    }
    async getTokenPublicKey() {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        return await this.sparkWallet.getIdentityPublicKey();
    }
    /**
     * Gets token balance and number of held leaves.
     * @returns An object containing the token balance and the number of owned leaves
     */
    async getTokenBalance() {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        return await this.sparkWallet.getIssuerTokenBalance();
    }
    /**
     * Mints new tokens to the specified address
     * TODO: Add support for minting directly to recipient address.
     */
    async mintTokens(amountToMint) {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        await this.sparkWallet.mintIssuerTokens(amountToMint);
    }
    /**
     * Transfers tokens to the specified receipient.
     */
    async transferTokens(amountToTransfer, recipientPublicKey) {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        await this.sparkWallet.transferIssuerTokens(amountToTransfer, recipientPublicKey);
    }
    /**
     * Consolidate all leaves into a single leaf.
     */
    async consolidateTokens() {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        await this.sparkWallet.consolidateIssuerTokenLeaves();
    }
    /**
     * Burns issuer tokens at the specified receipient.
     */
    async burnTokens(amountToBurn) {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        await this.sparkWallet.burnIssuerTokens(amountToBurn);
    }
    /**
     * Freezes tokens at the specified public key.
     */
    async freezeTokens(freezePublicKey) {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        return await this.sparkWallet.freezeIssuerTokens(freezePublicKey);
    }
    /**
     * Unfreezes tokens at the specified public key.
     */
    async unfreezeTokens(unfreezePublicKey) {
        if (!this.isSparkInitialized()) {
            throw new Error("Spark wallet not initialized");
        }
        return await this.sparkWallet.unfreezeIssuerTokens(unfreezePublicKey);
    }
}
//# sourceMappingURL=issuer-sdk.js.map