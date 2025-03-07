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
  private sparkWallet: IssuerSparkWallet;
  private initialized: boolean = false;

  private tokenPublicKeyInfo: lrc20sdk.TokenPubkeyInfo | undefined;

  constructor(network: Network) {
    this.sparkWallet = new IssuerSparkWallet(network);
  }

  async initWallet(
    mnemonicOrSeed?: Uint8Array | string,
    // Set to true to enable L1 Token Announcements.
    enableL1Wallet: boolean = true,
    lrc20WalletApiConfig?: lrc20sdk.LRC20WalletApiConfig,
  ): Promise<{
    balance: bigint;
    tokenBalance: Map<string, {
        balance: bigint;
    }>;
    mnemonic?: string | undefined;
  }> {
      let result = await this.sparkWallet.initWallet(mnemonicOrSeed, enableL1Wallet, lrc20WalletApiConfig);

      this.initialized = true;

      return result;
  }

  getSparkWallet(): IssuerSparkWallet {
    if (!this.initialized || !this.sparkWallet) {
      throw new Error("Spark wallet not initialized");
    }
    return this.sparkWallet;
  }

  getLRC20Wallet(): lrc20sdk.LRCWallet {
    if (!this.isL1Initialized()) {
      throw new Error("Bitcoin wallet not initialized");
    }

    return this.sparkWallet.getLRC20Wallet()!;
  }

  isSparkInitialized(): boolean {
    return this.initialized;
  }

  isL1Initialized(): boolean {
    return this.initialized && this.sparkWallet.getLRC20Wallet() !== undefined;
  }

  getL1FundingAddress(): string {
    if (!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }
    return this.getLRC20Wallet().p2wpkhAddress;
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
  async announceTokenL1(tokenName: string, tokenTicker: string, decimals: number, maxSupply: bigint, isFreezable: boolean): Promise<{txid: string} | undefined> {
    if(!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }
    let bitcoinWallet = this.getLRC20Wallet()!;

    try {
      return await announceTokenL1(bitcoinWallet, tokenName, tokenTicker, decimals, maxSupply, isFreezable)
    } catch(err: any) {
      if (err.message === "Not enough UTXOs") {
        console.error(
          "Error: No L1 UTXOs available to cover announcement fees. Please send sats to the address associated with your Issuer Wallet:",
          bitcoinWallet.p2wpkhAddress
        );
      } else {
        console.error("Unexpected error:", err);
      }
      return
    }
  }

  /**
   * Withdraws LRC20 tokens to L1
   */
  async withdrawTokens(receiverPublicKey?: string): Promise<{txid: string} | undefined> {
    if(!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }

    let tokenPublicKey = bytesToHex(this.getLRC20Wallet()!.pubkey);

    try {
      return await this.sparkWallet.withdrawTokens(tokenPublicKey, receiverPublicKey)
    } catch(err: any) {
      if (err.message === "Not enough UTXOs") {
        console.error(
          "Error: No L1 UTXOs available to cover exit fees. Please send sats to the address associated with your Issuer Wallet:",
          this.getLRC20Wallet()!.p2wpkhAddress
        );
      } else {
        console.error("Unexpected error:", err);
      }
      return
    }
  }

  /**
   * Gets LRC20 token info
   */
  async getTokenPublicKeyInfo(): Promise<lrc20sdk.TokenPubkeyInfo | undefined> {
    if (!this.isL1Initialized()) {
      throw new Error("L1 wallet not initialized");
    }

    if (this.tokenPublicKeyInfo) {
      return this.tokenPublicKeyInfo
    }

    const wallet = this.getLRC20Wallet();

    let tokenPublicKey = bytesToHex(wallet.pubkey);

    this.tokenPublicKeyInfo = await wallet.getTokenPubkeyInfo(tokenPublicKey);

    return this.tokenPublicKeyInfo
  }
}
