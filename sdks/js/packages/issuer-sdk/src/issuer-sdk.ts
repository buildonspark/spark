import {
  LRCWallet,
  NetworkType,
  TokenPubkey,
  TokenPubkeyAnnouncement,
  TokenPubkeyInfo,
} from "@buildonspark/lrc20-sdk";
import { SparkWallet, SparkWalletProps } from "@buildonspark/spark-sdk";
import { LeafWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { networks } from "bitcoinjs-lib";
import { IssuerWalletInterface } from "./interface/wallet-interface.js";
import { TokenFreezeService } from "./services/freeze.js";
import { IssuerTokenTransactionService } from "./services/token-transactions.js";

const BURN_ADDRESS = "02".repeat(33);

export class IssuerSparkWallet
  extends SparkWallet
  implements IssuerWalletInterface
{
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;
  private tokenPublicKeyInfo?: TokenPubkeyInfo;

  public static async create({
    ...props
  }: SparkWalletProps & { privateKey: string }) {
    const wallet = new IssuerSparkWallet(props.privateKey, props.options);

    const initResponse = await wallet.initWallet(props.mnemonicOrSeed);
    return {
      wallet,
      ...initResponse,
    };
  }

  private constructor(privateKey: string, configOptions?: ConfigOptions) {
    super(configOptions);

    // TODO: For now
    this.lrc20Wallet = new LRCWallet(
      privateKey,
      networks.regtest,
      NetworkType.REGTEST,
    );

    this.issuerTokenTransactionService = new IssuerTokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
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

  async announceTokenL1({
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

  mintTokensL1(tokenAmount: bigint): Promise<string> {
    throw new Error("Not implemented");
  }

  transferTokensL1(tokenAmount: bigint, p2trAddress: string): Promise<string> {
    throw new Error("Not implemented");
  }

  async getTokenPublicKeyInfo(): Promise<TokenPubkeyInfo> {
    if (this.tokenPublicKeyInfo) {
      return this.tokenPublicKeyInfo;
    }

    let tokenPublicKey = bytesToHex(this.lrc20Wallet!.pubkey);

    this.tokenPublicKeyInfo =
      await this.lrc20Wallet!.getTokenPubkeyInfo(tokenPublicKey);
    return this.tokenPublicKeyInfo;
  }
}
