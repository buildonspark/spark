import {
  LRCWallet,
  TokenPubkey,
  TokenPubkeyAnnouncement,
  TokenPubkeyInfo,
} from "@buildonspark/lrc20-sdk";
import {
  ListAllTokenTransactionsCursor,
  OperationType,
} from "@buildonspark/lrc20-sdk/proto/rpc/v1/types";
import { SparkWallet, SparkWalletProps } from "@buildonspark/spark-sdk";
import { encodeSparkAddress } from "@buildonspark/spark-sdk/address";
import { OutputWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import {
  getMasterHDKeyFromSeed,
  LRC_WALLET_NETWORK,
  LRC_WALLET_NETWORK_TYPE,
  Network,
} from "@buildonspark/spark-sdk/utils";
import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { TokenFreezeService } from "./services/freeze.js";
import { IssuerTokenTransactionService } from "./services/token-transactions.js";
import { GetTokenActivityResponse, TokenPubKeyInfoResponse } from "./types.js";
import {
  convertTokenActivityToHexEncoded,
  convertToTokenPubKeyInfoResponse,
} from "./utils/type-mappers.js";
import { decodeSparkAddress } from "@buildonspark/spark-sdk/address";

const BURN_ADDRESS = "02".repeat(33);

export type IssuerTokenInfo = {
  tokenPublicKey: string;
  tokenName: string;
  tokenSymbol: string;
  tokenDecimals: number;
  maxSupply: bigint;
  isFreezable: boolean;
};

export class IssuerSparkWallet extends SparkWallet {
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;
  private tokenPublicKeyInfo?: TokenPubkeyInfo;

  public static async initialize(options: SparkWalletProps) {
    const wallet = new IssuerSparkWallet(options.options);

    const initResponse = await wallet.initWallet(options.mnemonicOrSeed);
    return {
      wallet,
      ...initResponse,
    };
  }

  private constructor(configOptions?: ConfigOptions) {
    super(configOptions);
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

  public async getIssuerTokenInfo(): Promise<IssuerTokenInfo | null> {
    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    const tokenInfo = await lrc20Client.getTokenPubkeyInfo({
      publicKeys: [hexToBytes(await super.getIdentityPublicKey())],
    });

    const info = tokenInfo.tokenPubkeyInfos[0];
    return {
      tokenPublicKey: bytesToHex(info.announcement!.publicKey!.publicKey),
      tokenName: info.announcement!.name,
      tokenSymbol: info.announcement!.symbol,
      tokenDecimals: Number(bytesToNumberBE(info.announcement!.decimal)),
      isFreezable: info.announcement!.isFreezable,
      maxSupply: bytesToNumberBE(info.totalSupply),
    };
  }

  public async getIssuerTokenPublicKey() {
    return await super.getIdentityPublicKey();
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
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string> {
    const burnAddress = encodeSparkAddress({
      identityPublicKey: BURN_ADDRESS,
      network: this.config.getNetworkType(),
    });
    return await this.transferTokens({
      tokenPublicKey: await super.getIdentityPublicKey(),
      tokenAmount,
      receiverSparkAddress: burnAddress,
      selectedOutputs,
    });
  }

  public async freezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenOutputs();
    const tokenPublicKey = await super.getIdentityPublicKey();
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );
    const response = await this.tokenFreezeService!.freezeTokens(
      hexToBytes(decodedOwnerPubkey),
      hexToBytes(tokenPublicKey),
    );

    // Convert the Uint8Array to a bigint
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedOutputIds: response.impactedOutputIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  public async unfreezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenOutputs();
    const tokenPublicKey = await super.getIdentityPublicKey();
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );
    const response = await this.tokenFreezeService!.unfreezeTokens(
      hexToBytes(decodedOwnerPubkey),
      hexToBytes(tokenPublicKey),
    );
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedOutputIds: response.impactedOutputIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  public async getTokenActivity(
    pageSize: number = 100,
    cursor?: ListAllTokenTransactionsCursor,
    operationTypes?: OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<GetTokenActivityResponse> {
    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    const transactions = await lrc20Client.listTransactions({
      tokenPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      cursor,
      pageSize,
      beforeTimestamp,
      afterTimestamp,
      operationTypes,
    });

    return convertTokenActivityToHexEncoded(transactions);
  }

  public async getIssuerTokenActivity(
    pageSize: number = 100,
    cursor?: ListAllTokenTransactionsCursor,
    operationTypes?: OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<GetTokenActivityResponse> {
    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    const transactions = await lrc20Client.listTransactions({
      tokenPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      ownerPublicKey: hexToBytes(await super.getIdentityPublicKey()),
      cursor,
      pageSize,
      beforeTimestamp,
      afterTimestamp,
      operationTypes,
    });

    return convertTokenActivityToHexEncoded(transactions);
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
}
