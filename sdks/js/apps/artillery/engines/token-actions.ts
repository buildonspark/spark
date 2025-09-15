import { WalletPoolManager } from "./wallet-pool-manager";
import type { SparkContext, ArtilleryEventEmitter, EngineStep } from "./types";
import { walletPools } from "./hooks";
import { UserTokenMetadata } from "@buildonspark/spark-sdk";
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

type TokenParams = {
  walletName?: string;
  tokenName?: string;
  tokenSymbol?: string;
  totalSupply?: number;
  decimals?: number;
  isFreezable?: boolean;
};

export class TokenActions {
  private poolManager: WalletPoolManager;
  private engine: any; // Reference to parent engine

  constructor(
    private ee: ArtilleryEventEmitter,
    engine?: any,
  ) {
    this.poolManager = WalletPoolManager.getInstance();
    this.engine = engine;
  }

  mintToken(params: {
    walletName?: string;
    tokenId?: string;
    amount: number;
    recipient?: string; // Recipient wallet name or address
  }): EngineStep {
    return async (context: SparkContext, callback) => {
      const startTime = Date.now();

      try {
        console.log(
          `MintToken: Looking for wallet "${params.walletName || "default"}"`,
        );
        console.log(
          `  Available wallets in context.vars:`,
          context.vars ? Object.keys(context.vars) : "undefined",
        );

        const walletInfo = params.walletName
          ? context.vars?.[params.walletName]
          : context.sparkWallet;
        if (!walletInfo) {
          console.error(
            `  ERROR: Wallet "${params.walletName || "default"}" not found in context`,
          );
          console.error(
            `  context.vars keys:`,
            context.vars ? Object.keys(context.vars) : "undefined",
          );
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        // Note: IssuerSparkWallet tracks its own token, no need for tokenId
        console.log(
          `Minting ${params.amount} tokens from ${walletInfo.name}...`,
        );

        // Mint the tokens (mints to self only)
        const mintResult = await walletInfo.wallet.mintTokens(
          BigInt(params.amount),
        );

        // Get token balance to retrieve tokenIdentifier for V2 flow
        const tokenBalance = await walletInfo.wallet.getIssuerTokenBalance();

        // Store mint info in context
        context.vars = context.vars || {};
        context.vars.lastMintAmount = params.amount;
        context.vars.lastMintTxId = mintResult;
        if (tokenBalance.tokenIdentifier) {
          context.vars.tokenIdentifier = tokenBalance.tokenIdentifier;
        }

        console.log(`Tokens minted! Amount: ${params.amount}`);
        console.log(`Mint TX ID: ${mintResult}`);
        console.log(`Note: No mining required for token minting`);

        const mintTime = Date.now() - startTime;
        console.log(`Mint operation took ${mintTime}ms`);

        // Store metrics in context for processor to pick up
        context.vars.mintTime = mintTime;

        // Emit metrics to the scenario event emitter if available
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          console.log(
            `Emitting metrics to scenario EE: mint_token_time=${mintTime}, tokens_minted=1, amount=${params.amount}`,
          );
          scenarioEE.emit("histogram", "spark.mint_token_time", mintTime);
          scenarioEE.emit("counter", "spark.tokens_minted", 1);
          scenarioEE.emit(
            "counter",
            "spark.tokens_minted_amount",
            params.amount,
          );
          scenarioEE.emit("counter", "spark.mint_token_success", 1);
        } else {
          console.warn("No scenario event emitter available for metrics");
          this.ee.emit("histogram", "spark.mint_token_time", mintTime);
          this.ee.emit("counter", "spark.tokens_minted", 1);
          this.ee.emit("counter", "spark.tokens_minted_amount", params.amount);
          this.ee.emit("counter", "spark.mint_token_success", 1);
        }

        callback(null, context);
      } catch (error) {
        console.error("Token minting failed:", error.message);

        // Emit failure metrics
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          scenarioEE.emit("counter", "spark.token_minting_failed", 1);
          scenarioEE.emit("counter", "spark.mint_token_failed", 1);
        } else {
          this.ee.emit("counter", "spark.token_minting_failed", 1);
          this.ee.emit("counter", "spark.mint_token_failed", 1);
        }

        callback(error);
      }
    };
  }

  // Transfer tokens (V2 flow)
  transferToken(params: {
    walletName?: string;
    receiverName?: string;
    receiverAddress?: string;
    tokenId?: string;
    amount: number;
  }): EngineStep {
    return async (context: SparkContext, callback) => {
      const startTime = Date.now();

      try {
        const walletInfo = params.walletName
          ? context.vars?.[params.walletName]
          : context.sparkWallet;
        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        // Determine receiver address
        let receiverAddress: string;
        let receiverWallet: IssuerSparkWallet;
        if (params.receiverName) {
          receiverWallet = context.vars?.[params.receiverName].wallet;
          if (!receiverWallet) {
            throw new Error(`Receiver wallet ${params.receiverName} not found`);
          }
          receiverAddress = await receiverWallet.getSparkAddress();
          console.log(
            `Transferring tokens to named wallet: ${params.receiverName}`,
          );
        } else if (params.receiverAddress) {
          receiverAddress = params.receiverAddress;
        } else {
          throw new Error(
            "No receiver specified. Provide receiverName or receiverAddress",
          );
        }
        const senderWallet: IssuerSparkWallet = walletInfo.wallet;

        // Get token identifier issued by sender for V2 flow
        let senderTokenBalance = await senderWallet.getIssuerTokenBalance();
        if (!senderTokenBalance.tokenIdentifier) {
          senderTokenBalance = await receiverWallet.getIssuerTokenBalance();

          if (!senderTokenBalance.tokenIdentifier) {
            throw new Error(
              "Token identifier not found. Make sure the token is created and you have a balance.",
            );
          }
        }

        console.log(
          `Transferring ${params.amount} tokens from ${walletInfo.name} to ${receiverAddress.substring(0, 10)}...`,
        );

        const { tokenBalances: receiverTokenBalances } =
          await receiverWallet.getBalance();

        let tokenBalanceForReceiver: {
          balance: bigint;
          tokenMetadata?: UserTokenMetadata;
        };
        if (receiverTokenBalances.has(senderTokenBalance.tokenIdentifier)) {
          tokenBalanceForReceiver = receiverTokenBalances.get(
            senderTokenBalance.tokenIdentifier,
          );
        } else {
          tokenBalanceForReceiver = {
            balance: 0n,
          };
        }
        console.log(
          ` Token balance before token (Token ID: ${senderTokenBalance.tokenIdentifier}) transfer: ${tokenBalanceForReceiver.balance}`,
        );

        // Transfer the tokens using V2 flow with tokenIdentifier
        const transferResult = await senderWallet.transferTokens({
          tokenIdentifier: senderTokenBalance.tokenIdentifier,
          tokenAmount: BigInt(params.amount),
          receiverSparkAddress: receiverAddress,
        });

        console.log(`Token transfer completed! TX ID: ${transferResult}`);
        console.log(`Note: No mining required for token transfers`);

        const transferTime = Date.now() - startTime;
        console.log(`Transfer operation took ${transferTime}ms`);

        await new Promise((resolve) => setTimeout(resolve, 10000));

        const tokenTxs = await senderWallet.queryTokenTransactions({
          tokenTransactionHashes: [transferResult],
        });

        if (tokenTxs.tokenTransactionsWithStatus.length == 0) {
          throw new Error(
            `Failed to get token transaction status for TX with ID: ${transferResult}`,
          );
        }

        console.log(` Token transfer tx status: ${tokenTxs[0]} `);

        const { tokenBalances: newReceiverTokenBalances } =
          await receiverWallet.getBalance();

        console.log(
          ` Token balance after token transfer: ${newReceiverTokenBalances.get(senderTokenBalance.tokenIdentifier).balance}`,
        );
        // Store metrics in context for processor to pick up
        context.vars.transferTime = transferTime;
        context.vars.transferAmount = params.amount;

        // Emit metrics to the scenario event emitter if available
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          console.log(
            `Emitting metrics to scenario EE: token_transfer_time=${transferTime}, tokens_transferred=${params.amount}`,
          );
          scenarioEE.emit(
            "histogram",
            "spark.token_transfer_time",
            transferTime,
          );
          scenarioEE.emit("counter", "spark.token_transfer_success", 1);
          scenarioEE.emit("counter", "spark.tokens_transferred", params.amount);
        } else {
          // Fallback to instance event emitter
          this.ee.emit("histogram", "spark.token_transfer_time", transferTime);
          this.ee.emit("counter", "spark.token_transfer_success", 1);
          this.ee.emit("counter", "spark.tokens_transferred", params.amount);
        }

        callback(null, context);
      } catch (error) {
        console.error("Token transfer failed:", error.message);

        // Emit failure metrics
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          scenarioEE.emit("counter", "spark.token_transfer_failed", 1);
        } else {
          this.ee.emit("counter", "spark.token_transfer_failed", 1);
        }

        callback(error);
      }
    };
  }

  // Create a new tokens for all wallets in pool (V2 flow)
  announceTokensForPool(params: { poolName: string }): EngineStep {
    return async (context: SparkContext, callback) => {
      const ee = this.engine?.scenarioEE ?? this.ee;

      try {
        const wallets = walletPools.get(params.poolName).wallets;
        for (const wallet of wallets) {
          const addr = await wallet.getSparkAddress();
          console.log(`Announcing token for ${addr}`);

          let tokenId: string;
          try {
            tokenId = await wallet.getIssuerTokenIdentifier();
            if (tokenId) {
              // token is set for this wallet
              ee.emit("counter", "spark.token_already_exists", 1);
              continue;
            }
          } catch (err) {
            // If getIssuerTokenIdentifier fails, we'll try to create the token anyway
            // as it might not exist yet. If there's a real error, createToken will fail.
            console.log(
              `Token may not exist for ${addr}, attempting to create...`,
            );
          }

          let creationResult: string;
          try {
            creationResult = await wallet.createToken({
              tokenName: "TEST_TOKEN",
              tokenTicker: "TST",
              decimals: 8,
              maxSupply: BigInt(0),
              isFreezable: false,
            });
          } catch (tokenError: any) {
            const error: Error = tokenError;
            console.log(`message: ${error.message}`);
            console.log(
              `Failed to create token for ${addr}, error: ${error.message}`,
            );
            ee.emit("counter", "spark.token_announce_failed", 1);
            continue;
          }

          console.log(`Token creation TX ID: ${creationResult}`);
          console.log(`Token announced successfully! Now you can mint tokens.`);

          ee.emit("counter", "spark.token_announced", 1);
        }

        callback(null, context);
      } catch (err) {
        console.log(
          `Failed to announce tokens for wallets in pool: ${err.message}`,
        );
        callback(err);
        ee.emit("counter", "spark.token_announce_failed", 1);
      }
    };
  }

  // Create a new token (V2 flow)
  announceToken({
    walletName,
    tokenName = "TEST_TOKEN",
    tokenSymbol = "TEST",
    totalSupply = 0,
    decimals = 8,
    isFreezable = false,
  }: TokenParams = {}): EngineStep {
    return async (context: SparkContext, callback) => {
      const startTime = Date.now();
      const ee = this.engine?.scenarioEE ?? this.ee;

      try {
        const walletInfo = walletName
          ? context.vars?.[walletName]
          : context.sparkWallet;
        if (!walletInfo) {
          throw new Error(`Wallet ${walletName || "default"} not found`);
        }

        console.log(
          `Creating token ${tokenSymbol} (${tokenName}) from ${walletInfo.name}...`,
        );
        console.log(
          `Max supply: ${totalSupply}, Decimals: ${decimals || 8}, Freezable: ${isFreezable || false}`,
        );

        let creationResult: string;
        try {
          creationResult = await walletInfo.wallet.createToken({
            tokenName: tokenName,
            tokenTicker: tokenSymbol,
            decimals: decimals || 8,
            maxSupply: BigInt(totalSupply),
            isFreezable: isFreezable || false,
          });
        } catch (tokenError: any) {
          console.log(
            `Failed to announce tokens for wallet ${walletName} in pool: ${tokenError}`,
          );
          ee.emit("counter", "spark.token_announce_failed", 1);
          callback(null, context);
          return;
        }

        context.vars = context.vars || {};
        context.vars.tokenSymbol = tokenSymbol;
        context.vars.tokenName = tokenName;
        context.vars.tokenTicker = tokenSymbol;
        context.vars.creationTxId = creationResult;

        console.log(`Token creation TX ID: ${creationResult}`);
        console.log(`Token announced successfully! Now you can mint tokens.`);

        const announceTime = Date.now() - startTime;
        console.log(`Create operation took ${announceTime}ms`);
        context.vars.announceTime = announceTime;

        ee.emit("histogram", "spark.announce_token_time", announceTime);
        ee.emit("counter", "spark.token_announced", 1);

        callback(null, context);
      } catch (error: any) {
        console.error(
          "Token creation failed:",
          error?.message || String(error),
        );
        ee.emit("counter", "spark.token_announce_failed", 1);
        callback(null, context);
      }
    };
  }
}
