import {
  LRCWallet,
  NetworkType,
  TokenPubkey,
  TokenPubkeyAnnouncement,
  TokenPubkeyInfo,
} from "@buildonspark/lrc20-sdk";
import { SparkWallet, SparkWalletProps } from "@buildonspark/spark-sdk";
import { LeafWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark";
import { getMasterHDKeyFromSeed } from "@buildonspark/spark-sdk/utils";
import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { networks } from "bitcoinjs-lib";
import { IssuerWalletInterface } from "./interface/wallet-interface.js";
import { TokenFreezeService } from "./services/freeze.js";
import { IssuerTokenTransactionService } from "./services/token-transactions.js";
import { ListAllTokenTransactionsResponse, ListAllTokenTransactionsCursor } from "@buildonspark/lrc20-sdk"
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import { createLrc20ConnectionManager, Lrc20SparkClient } from "@buildonspark/lrc20-sdk/grpc";
import { ILrc20ConnectionManager } from "@buildonspark/lrc20-sdk/grpc";
import { OperationType } from "../../lrc20-sdk/dist/types/proto/rpc/v1/types.js";

const BURN_ADDRESS = "02".repeat(33);

export class IssuerSparkWallet extends SparkWallet implements IssuerWalletInterface {
  private lrc20ConnectionManager: ILrc20ConnectionManager;
  private lrc20Client: Lrc20SparkClient | undefined;
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;
  private tokenPublicKeyInfo?: TokenPubkeyInfo;


  public static async create(options: SparkWalletProps) {
    const wallet = new IssuerSparkWallet(options.options);

    const initResponse = await wallet.initIssuerWallet(options.mnemonicOrSeed);
    return {
      wallet,
      ...initResponse,
    };
  }

  private constructor(configOptions?: ConfigOptions) {
    super(configOptions);
    this.lrc20ConnectionManager = createLrc20ConnectionManager(this.config.getLrc20Address());
    this.issuerTokenTransactionService = new IssuerTokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
  }

  private async initIssuerWallet(mnemonicOrSeed?: string | Uint8Array) {
    const initResponse = await super.initWallet(mnemonicOrSeed);

    this.lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    // TODO: Remove this in subsequent PRs when LRC20Wallet has a proper signer interface
    mnemonicOrSeed = mnemonicOrSeed || initResponse?.mnemonic;
    if (mnemonicOrSeed) {
      let seed: Uint8Array;
      if (typeof mnemonicOrSeed !== "string") {
        seed = mnemonicOrSeed;
      } else {
        if (validateMnemonic(mnemonicOrSeed, wordlist)) {
          seed = await this.config.signer.mnemonicToSeed(mnemonicOrSeed);
        } else {
          seed = hexToBytes(mnemonicOrSeed);
        }
      }

      const hdkey = getMasterHDKeyFromSeed(seed);

      this.lrc20Wallet = new LRCWallet(
        bytesToHex(hdkey.privateKey!),
        networks.regtest,
        NetworkType.REGTEST,
      );
    }

    return initResponse;
  }

  public async getIssuerTokenPublicKey() {
    return await super.getIdentityPublicKey();
  }

  public async getIssuerTokenBalance(): Promise<{
    balance: bigint;
  }> {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();

    if (!balanceObj.tokenBalances || !balanceObj.tokenBalances.has(publicKey)) {
      return {
        balance: 0n,
      };
    }
    return {
      balance: balanceObj.tokenBalances.get(publicKey)!.balance,
    };
  }

  public async mintTokens(tokenAmount: bigint): Promise<string> {
    var tokenPublicKey = await super.getIdentityPublicKey();

    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        hexToBytes(tokenPublicKey),
        tokenAmount,
      );

    return await this.issuerTokenTransactionService.broadcastTokenTransaction(
      tokenTransaction,
    );
  }

  public async burnTokens(
    tokenAmount: bigint,
    selectedLeaves?: LeafWithPreviousTransactionData[],
  ): Promise<string> {
    return await this.transferTokens({
      tokenPublicKey: await super.getIdentityPublicKey(),
      tokenAmount,
      receiverSparkAddress: BURN_ADDRESS,
      selectedLeaves,
    });
  }

  public async freezeTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.freezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey),
    );

    // Convert the Uint8Array to a bigint
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  public async unfreezeTokens(
    ownerPublicKey: string,
  ): Promise<{ impactedLeafIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenLeaves();
    const tokenPublicKey = await super.getIdentityPublicKey();

    const response = await this.tokenFreezeService!.unfreezeTokens(
      hexToBytes(ownerPublicKey),
      hexToBytes(tokenPublicKey),
    );
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedLeafIds: response.impactedLeafIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  public async getTokenActivity(
    pageSize: number = 100,
    cursor?: ListAllTokenTransactionsCursor,
    operationTypes?: OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<ListAllTokenTransactionsResponse> {
    if (!this.lrc20Client) {
      throw new Error("LRC20 client not initialized");
    }

    const transactions = await this.lrc20Client.listTransactions({
      tokenPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      cursor,
      pageSize,
      beforeTimestamp,
      afterTimestamp,
      operationTypes,
    });
    return transactions
  }

  public async getIssuerTokenActivity(
    pageSize: number = 100,
    cursor?: ListAllTokenTransactionsCursor,
    operationTypes?: OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<ListAllTokenTransactionsResponse> {
    if (!this.lrc20Client) {
      throw new Error("LRC20 client not initialized");
    }

    const transactions = await this.lrc20Client.listTransactions({
      tokenPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      ownerPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      cursor,
      pageSize,
      beforeTimestamp,
      afterTimestamp,
      operationTypes,
    });
    return transactions
  }

  public getL1Address(): string {
    if (!this.lrc20Wallet) {
      throw new Error("L1 Wallet not initialized");
    }
    return this.lrc20Wallet.p2wpkhAddress;
  }

  public async announceTokenL1({
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
    feeRateSatsPerVb = 2.0,
  }): Promise<string> {
    await this.lrc20Wallet!.syncWallet();

    const tokenPublicKey = new TokenPubkey(this.lrc20Wallet!.pubkey);

    const announcement = new TokenPubkeyAnnouncement(
      tokenPublicKey,
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezable,
    );

    const tx = await this.lrc20Wallet!.prepareAnnouncement(
      announcement,
      feeRateSatsPerVb,
    );

    return await this.lrc20Wallet!.broadcastRawBtcTransaction(
      tx.bitcoin_tx.toHex(),
    );
  }

  public mintTokensL1(tokenAmount: bigint): Promise<string> {
    throw new Error("Not implemented");
  }

  public transferTokensL1(
    tokenAmount: bigint,
    p2trAddress: string,
  ): Promise<string> {
    throw new Error("Not implemented");
  }

  public async getTokenPublicKeyInfo(): Promise<TokenPubkeyInfo> {
    if (this.tokenPublicKeyInfo) {
      return this.tokenPublicKeyInfo;
    }

    let tokenPublicKey = bytesToHex(this.lrc20Wallet!.pubkey);

    this.tokenPublicKeyInfo =
      await this.lrc20Wallet!.getTokenPubkeyInfo(tokenPublicKey);
    return this.tokenPublicKeyInfo;
  }
}
