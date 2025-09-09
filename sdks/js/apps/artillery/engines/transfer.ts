// @ts-nocheck
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import {
  TransferParams,
  SparkContext,
  EngineStep,
  ArtilleryEventEmitter,
} from "./types/index.js";

export class TransferActions {
  private engine: any; // Reference to parent engine

  constructor(
    private ee: ArtilleryEventEmitter,
    engine?: any,
  ) {
    this.engine = engine;
  }

  transfer(params: {
    walletName?: string;
    receiverName?: string;
    amount?: number;
  }): EngineStep {
    return async (context: SparkContext, callback) => {
      const startTime = Date.now();

      if (context.vars.$loopCount) {
        console.log(
          ` Action in loop. Iteration number: ${context.vars.$loopCount}`,
        );
      }
      try {
        // Check if amount is specified
        let amount = params.amountSats || params.amount;
        if (
          amount === undefined &&
          context.vars?.transferAmount !== undefined
        ) {
          amount = context.vars.transferAmount;
        }

        if (amount === undefined) {
          throw new Error("Transfer amount must be specified");
        }

        const senderWalletInfo = params.walletName
          ? context.vars?.[params.walletName]
          : context.sparkWallet;
        if (!senderWalletInfo) {
          throw new Error(
            `Sender wallet ${params.walletName || "default"} not found`,
          );
        }

        // Get receiver address
        let receiverAddress: string;
        let receiverWallet: IssuerSparkWallet | undefined;
        if (params.receiverName) {
          // Check if we have a wallet name mapping (for concurrent scenarios)
          const receiverInfo = context.vars?.[params.receiverName];
          if (!receiverInfo) {
            throw new Error(
              `Receiver wallet ${params.receiverName} not found. It should be locked in beforeScenario`,
            );
          }
          receiverAddress = receiverInfo.address;
          receiverWallet = receiverInfo.wallet;
        } else if (params.receiverAddress) {
          receiverAddress = params.receiverAddress;
        } else if (context.vars?.receiverAddress) {
          receiverAddress = context.vars.receiverAddress;
        } else {
          throw new Error("No receiver specified");
        }

        console.log(
          `${senderWalletInfo.name} transferring ${amount} sats to ${params.receiverName || receiverAddress}...`,
        );

        console.log(`\n Receiver spark address: ${receiverAddress}\n`);

        const result = await (
          senderWalletInfo.wallet as IssuerSparkWallet
        ).transfer({
          amountSats: amount,
          receiverSparkAddress: receiverAddress,
        });

        console.log(
          `Transfer from ${senderWalletInfo.name} completed: ${amount} sats`,
        );

        const transferTime = Date.now() - startTime;
        console.log(`Transfer operation took ${transferTime}ms`);

        // Store transfer amount in context for balance calculations
        context.vars = context.vars || {};
        context.vars[params.receiverName + "_transferInfo"] = {
          receiverWallet: receiverWallet,
          transferID: result.id,
        };
        context.vars.lastTransferAmount = amount;
        context.vars.transferTime = transferTime;

        // Emit metrics to the scenario event emitter if available
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          console.log(
            `Emitting metrics to scenario EE: transfer_time=${transferTime}, amount=${amount} sats`,
          );
          scenarioEE.emit("histogram", "spark.transfer_time", transferTime);
          scenarioEE.emit("counter", "spark.transfer_success", 1);
          scenarioEE.emit("counter", "spark.sats_transferred", amount);
        } else {
          console.warn("No scenario event emitter available for metrics");
        }

        callback(null, context);
      } catch (error: any) {
        console.error("Transfer failed:", error.message);
        if (error.cause) {
          console.error("Caused by:", error.cause);
        }
        if (error.graphQLErrors) {
          console.error("GraphQL errors:", error.graphQLErrors);
        }
        if (error.networkError) {
          console.error("Network error:", error.networkError);
        }

        // Emit failure metrics
        const scenarioEE = this.engine?.scenarioEE;
        if (scenarioEE) {
          scenarioEE.emit("counter", "spark.transfer_failed", 1);
        } else {
          this.ee.emit("counter", "spark.transfer_failed", 1);
        }

        callback(error);
      }
    };
  }

  claimTransfer(params: { walletName: string }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      const startTime = Date.now();

      try {
        const walletInfo: {
          receiverWallet: IssuerSparkWallet | undefined;
          transferID: string;
        } = context.vars?.[params.walletName + "_transferInfo"];

        if (!walletInfo || !walletInfo.receiverWallet) {
          console.error(
            `  ERROR: Wallet "${params.walletName}" not found in context`,
          );
          console.error(
            `  context.vars keys:`,
            context.vars ? Object.keys(context.vars) : "undefined",
          );
          throw new Error(`Wallet ${params.walletName} not found`);
        }

        // Create necessary services for the receiver wallet
        const receiverWallet = walletInfo.receiverWallet;
        const sparkAddress = await receiverWallet.getSparkAddress();

        const { balance: balanceBefore } = await receiverWallet.getBalance();
        console.log(
          `   Balance before transfer claim for wallet ${sparkAddress.substring(0, 10)}: ${balanceBefore}`,
        );

        const receiverTransferService = (receiverWallet as any).transferService;
        let pendingTransfer = await receiverTransferService.queryTransfer(
          walletInfo.transferID,
        );

        if (!pendingTransfer) {
          console.log(` Transfer not found (ID: ${q.transferId})`);
          throw new Error(`Transfer not found (ID: ${q.transferId})`);
        }

        await (receiverWallet as any).claimTransfer({
          transfer: pendingTransfer,
          optimize: true,
        });

        await new Promise((resolve) => setTimeout(resolve, 2000));

        // Store result if requested
        if (params.storeAs) {
          context.vars[params.storeAs] = {
            walletName: params.walletName,
            transferCount: pendingTransfers.length,
            success: true,
          };
        }

        const { balance: balanceAfter } = await receiverWallet.getBalance();
        const transferTime = Date.now() - startTime;

        context.vars = context.vars || {};
        console.log(
          `   Balance after transfer claim for wallet ${sparkAddress.substring(0, 10)}: ${balanceAfter}`,
        );
        console.log(`   Transfer operation took ${transferTime}ms`);
        console.log(
          `   Transfer claimed successfully for wallet ${sparkAddress.substring(0, 10)}`,
        );

        ee.emit(
          "histogram",
          "spark.claim_transfer_time",
          Date.now() - startTime,
        );
        ee.emit("counter", "spark.claim_transfer_success", 1);
        callback(null, context);
      } catch (error) {
        console.error(
          `failed to claim transfer ${params.walletName}:`,
          error.message,
        );
        ee.emit("counter", "spark.claim_transfer_failed", 1);
        callback(error);
      }
    };
  }
}
