import { hexToBytes /*, bytesToHex */ } from "@noble/curves/abstract/utils";
import { Network } from "@buildonspark/spark-js-sdk/utils";
import { IssuerSparkWallet } from "./services/spark/wallet.js";
// import * as bip39 from "@scure/bip39";
// import { HDKey } from "@scure/bip32";
// import { LRCWallet } from "lrc20-js-sdk";
// import { networks } from "bitcoinjs-lib";
// import { NetworkType } from "lrc20-js-sdk";

export class IssuerWallet {
  private bitcoinWallet: any | undefined;
  private sparkWallet: IssuerSparkWallet;
  private initialized: boolean = false;

  constructor(network: Network) {
    if (network !== Network.REGTEST) {
      throw new Error("Only REGTEST network is supported");
    }
    this.sparkWallet = new IssuerSparkWallet(network);
  }

  async generateMnemonic(): Promise<string> {
    if (!this.sparkWallet) {
      throw new Error("Wallet not initialized");
    }

    return this.sparkWallet?.generateMnemonic();
  }

  async createWallet(
    mnemonic: string,
    // Set to true to enable L1 Token Announcements.
    enableL1Wallet: boolean = true
  ): Promise<void> {
    await this.sparkWallet.createSparkWallet(mnemonic);

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

  getSparkWallet(): IssuerSparkWallet {
    if (!this.initialized || !this.sparkWallet) {
      throw new Error("Spark wallet not initialized");
    }
    return this.sparkWallet;
  }

  getBitcoinWallet(): any {
    if (!this.initialized || !this.bitcoinWallet) {
      throw new Error("Bitcoin wallet not initialized");
    }
    return this.sparkWallet !== undefined;
  }

  isSparkInitialized(): boolean {
    return this.initialized;
  }

  isL1Initialized(): boolean {
    return this.initialized && this.bitcoinWallet !== undefined;
  }

  /**
   * Mints new tokens to the specified address
   * TODO: Add support for minting to recipient address.
   */
  async mintTokens(amountToMint: bigint) {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.mintIssuerTokens(amountToMint);
    }
  }

  /**
   * Transfers tokens to the specified receipient.
   */
  async transferTokens(amountToTransfer: bigint, recipientPublicKey: string) {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.transferIssuerTokens(
        amountToTransfer,
        recipientPublicKey
      );
    }
  }

  async consolidateTokens() {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.consolidateIssuerTokenLeaves();
    }
  }

  /**
   * Burns issuer tokens at the specified receipient.
   */
  async burnTokens(amountToBurn: bigint) {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.burnIssuerTokens(amountToBurn);
    }
  }

  /**
   * Freezes tokens at the specified public key.
   */
  async freezeTokens(freezePublicKey: string) {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.freezeIssuerTokens(hexToBytes(freezePublicKey));
    }
  }

  /**
   * Unfreezes tokens at the specified public key.
   */
  async unfreezeTokens(unfreezePublicKey: string) {
    if (this.isSparkInitialized()) {
      await this.sparkWallet.unfreezeIssuerTokens(
        hexToBytes(unfreezePublicKey)
      );
    }
  }
}
