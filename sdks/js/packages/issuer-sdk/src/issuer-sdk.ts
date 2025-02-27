import lrc20sdk from "@buildonspark/lrc20-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";
import { bytesToHex } from "@noble/curves/abstract/utils";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { announceTokenL1 } from "./services/lrc20/announce.js";
import { IssuerSparkWallet } from "./services/spark/wallet.js";
import {
  LRC_WALLET_NETWORK,
  LRC_WALLET_NETWORK_TYPE,
} from "./utils/constants.js";

export class IssuerWallet {
  private bitcoinWallet: lrc20sdk.LRCWallet | undefined;
  private sparkWallet: IssuerSparkWallet;
  private initialized: boolean = false;
  private network: Network;

  constructor(network: Network) {
    this.sparkWallet = new IssuerSparkWallet(network);
    this.network = network;
  }

  async initWallet(
    mnemonicOrSeed?: Uint8Array | string,
    // Set to true to enable L1 Token Announcements.
    enableL1Wallet: boolean = true,
  ): Promise<void> {
    let result = await this.sparkWallet.initWallet(mnemonicOrSeed);

    if (enableL1Wallet) {
      if (!mnemonicOrSeed) {
        mnemonicOrSeed = result.mnemonic!;
      }

      let seed;
      if (typeof mnemonicOrSeed === "string") {
        seed = await bip39.mnemonicToSeed(mnemonicOrSeed);
      } else {
        seed = mnemonicOrSeed;
      }

      const hdkey = HDKey.fromMasterSeed(seed).derive("m/0").privateKey!;
      this.bitcoinWallet = new lrc20sdk.LRCWallet(
        bytesToHex(hdkey),
        LRC_WALLET_NETWORK[this.network],
        LRC_WALLET_NETWORK_TYPE[this.network],
      );
    }
    this.initialized = true;
  }

  getSparkWallet(): IssuerSparkWallet {
    if (!this.initialized || !this.sparkWallet) {
      throw new Error("Spark wallet not initialized");
    }
    return this.sparkWallet;
  }

  getBitcoinWallet(): lrc20sdk.LRCWallet {
    if (!this.initialized || !this.bitcoinWallet) {
      throw new Error("Bitcoin wallet not initialized");
    }
    return this.bitcoinWallet;
  }

  isSparkInitialized(): boolean {
    return this.initialized;
  }

  isL1Initialized(): boolean {
    return this.initialized && this.bitcoinWallet !== undefined;
  }

  getL1FundingAddress(): string {
    if (!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }
    return this.getBitcoinWallet().p2wpkhAddress;
  }

  async getTokenPublicKey(): Promise<string> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }
    return await this.sparkWallet.getIdentityPublicKey();
  }

  /**
   * Gets token balance and number of held leaves.
   * @returns An object containing the token balance and the number of owned leaves
   */
  async getBalance(): Promise<{ balance: bigint }> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }

    const publicKey = await this.sparkWallet.getIdentityPublicKey();
    const balanceObj = await this.sparkWallet.getBalance(true);
    if (!balanceObj.tokenBalances || !balanceObj.tokenBalances.has(publicKey)) {
      return {
        balance: 0n,
      };
    }
    return {
      balance: balanceObj.tokenBalances.get(publicKey)!.balance,
    };
  }

  /**
   * Mints new tokens to the specified address
   * TODO: Add support for minting directly to recipient address.
   */
  async mintTokens(amountToMint: bigint): Promise<string> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }

    return await this.sparkWallet.mintIssuerTokens(amountToMint);
  }

  /**
   * Transfers tokens to the specified receipient.
   */
  async transferTokens(
    amountToTransfer: bigint,
    receiverSparkAddress: string,
  ): Promise<string> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }

    return await this.sparkWallet.transferIssuerTokens(
      amountToTransfer,
      receiverSparkAddress,
    );
  }

  /**
   * Burns issuer tokens at the specified receipient.
   */
  async burnTokens(amountToBurn: bigint): Promise<string> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }

    return await this.sparkWallet.burnIssuerTokens(amountToBurn);
  }

  /**
   * Freezes tokens at the specified public key.
   */
  async freezeTokens(freezePublicKey: string): Promise<{
    impactedLeafIds: string[];
    impactedTokenAmount: bigint;
  }> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }
    return await this.sparkWallet.freezeIssuerTokens(freezePublicKey);
  }

  /**
   * Unfreezes tokens at the specified public key.
   */
  async unfreezeTokens(unfreezePublicKey: string): Promise<{
    impactedLeafIds: string[];
    impactedTokenAmount: bigint;
  }> {
    if (!this.isSparkInitialized()) {
      throw new Error("Spark wallet not initialized");
    }
    return await this.sparkWallet.unfreezeIssuerTokens(unfreezePublicKey);
  }

  /**
   * Announces LRC20 token on L1
   */
  async announceTokenL1(
    tokenName: string,
    tokenTicker: string,
    decimals: number,
    maxSupply: bigint,
    isFreezable: boolean,
  ): Promise<{ txid: string }> {
    if (!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }

    return await announceTokenL1(
      this.bitcoinWallet!,
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezable,
    );
  }
}
