import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { outputParam, type OutputMode } from "../utils.js";
import { resolveWallet, createFreshWallet } from "../wallet.js";
import type { ServerConfig } from "../config.js";
import { handleGetBalance, handleGetSparkAddress } from "./wallet.js";
import { handleGetDepositAddress, handleClaimDeposit } from "./deposits.js";
import {
  handleSendTransfer,
  handleGetTransfer,
  handleListTransfers,
} from "./transfers.js";
import {
  handleCreateInvoice,
  handlePayInvoice,
  handleGetLightningFeeEstimate,
} from "./lightning.js";
import { handleGetWithdrawalFeeQuote, handleWithdraw } from "./withdrawals.js";
import { handleFundAddress } from "./funding.js";
import { handleDeposit } from "./deposit-flow.js";
import { handleCreateWallet } from "./create-wallet.js";

const mnemonicParam = z
  .string()
  .optional()
  .describe(
    "BIP39 mnemonic for the wallet to use. Omit to use the server default (SPARK_MNEMONIC env var).",
  );

const networkParam = z
  .enum(["LOCAL", "REGTEST", "MAINNET"])
  .optional()
  .describe(
    "Bitcoin network for this call. LOCAL = self-hosted regtest (minikube/run-everything.sh), " +
      "REGTEST = Lightspark-hosted regtest, MAINNET = production Bitcoin. " +
      "Omit to use the server default.",
  );

/** Create a resolve function with the network override baked in. */
function makeResolve(network?: string) {
  return (mnemonic?: string) =>
    resolveWallet(
      mnemonic,
      undefined,
      network as "LOCAL" | "REGTEST" | "MAINNET" | undefined,
    );
}

/** Create a createFresh function with the network override baked in. */
function makeCreateFresh(network?: string) {
  return () =>
    createFreshWallet(
      undefined,
      network as "LOCAL" | "REGTEST" | "MAINNET" | undefined,
    );
}

export function registerAllTools(
  server: McpServer,
  config: ServerConfig,
): void {
  const isLocal = config.defaultNetwork === "LOCAL";

  // Wallet creation
  server.tool(
    "spark_create_wallet",
    "Generate a brand new Spark wallet. Returns the mnemonic (save it!) and Spark address. Pass the mnemonic to any other tool to operate on this wallet.",
    {
      network: networkParam,
      output: outputParam,
    },
    ({ network, output }: { network?: string; output?: OutputMode }) =>
      handleCreateWallet(makeCreateFresh(network), output),
  );

  // Wallet tools
  server.tool(
    "spark_get_balance",
    "Get the current wallet balance in satoshis.",
    {
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      mnemonic,
      network,
      output,
    }: {
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) => handleGetBalance(mnemonic, makeResolve(network), output),
  );
  server.tool(
    "spark_get_spark_address",
    "Get the wallet's Spark address for receiving transfers",
    {
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      mnemonic,
      network,
      output,
    }: {
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) => handleGetSparkAddress(mnemonic, makeResolve(network), output),
  );

  // Deposit tools
  server.tool(
    "spark_get_deposit_address",
    "Get a single-use Bitcoin address to deposit funds into the Spark wallet. IMPORTANT: Each deposit address can only be used once. After funding and claiming a deposit, you must call this again to get a fresh address for the next deposit.",
    {
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      mnemonic,
      network,
      output,
    }: {
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) => handleGetDepositAddress(mnemonic, makeResolve(network), output),
  );
  server.tool(
    "spark_claim_deposit",
    "Claim a confirmed on-chain Bitcoin deposit by transaction ID. Waits for the balance to settle before returning, so the funds are immediately spendable once this tool completes.",
    {
      txid: z.string().describe("The Bitcoin transaction ID of the deposit"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      txid,
      mnemonic,
      network,
      output,
    }: {
      txid: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleClaimDeposit(
        txid,
        mnemonic,
        makeResolve(network),
        undefined,
        output,
      ),
  );
  // Dev-only tools: only registered in LOCAL environments (where bitcoind RPC is available).
  if (isLocal) {
    server.tool(
      "spark_fund_address",
      "Fund a Bitcoin address using the local regtest node. Only works in LOCAL environments (run-everything.sh or minikube). Sends funds and mines blocks to confirm.",
      {
        address: z.string().describe("The Bitcoin address to fund"),
        amountSats: z
          .number()
          .int()
          .positive()
          .optional()
          .describe("Amount to send in satoshis (default: 50,000)"),
        blocksToMine: z
          .number()
          .int()
          .positive()
          .optional()
          .describe("Blocks to mine for confirmation (default: 1)"),
        network: networkParam,
        output: outputParam,
      },
      ({
        address,
        amountSats,
        blocksToMine,
        network,
        output,
      }: {
        address: string;
        amountSats?: number;
        blocksToMine?: number;
        network?: string;
        output?: OutputMode;
      }) =>
        handleFundAddress(
          address,
          amountSats,
          blocksToMine,
          undefined,
          undefined,
          output,
          network,
        ),
    );
    server.tool(
      "spark_deposit",
      "Fund a Spark wallet in one step: gets a fresh deposit address, funds it via the local regtest node, claims the deposit, and waits for the balance to settle. Only available in LOCAL environments. For other environments, use spark_get_deposit_address + external funding + spark_claim_deposit.",
      {
        amountSats: z
          .number()
          .int()
          .positive()
          .optional()
          .describe("Amount to deposit in satoshis (default: 50,000)"),
        mnemonic: mnemonicParam,
        network: networkParam,
        output: outputParam,
      },
      ({
        amountSats,
        mnemonic,
        network,
        output,
      }: {
        amountSats?: number;
        mnemonic?: string;
        network?: string;
        output?: OutputMode;
      }) =>
        handleDeposit(
          amountSats,
          mnemonic,
          makeResolve(network),
          undefined,
          output,
          network,
        ),
    );
  }

  // Transfer tools
  server.tool(
    "spark_send_transfer",
    "Send satoshis to a Spark address (off-chain, instant)",
    {
      receiverSparkAddress: z
        .string()
        .describe("The recipient's Spark address"),
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("Amount to send in satoshis"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      receiverSparkAddress,
      amountSats,
      mnemonic,
      network,
      output,
    }: {
      receiverSparkAddress: string;
      amountSats: number;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleSendTransfer(
        receiverSparkAddress,
        amountSats,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );
  server.tool(
    "spark_get_transfer",
    "Get the status and details of a specific transfer by ID",
    {
      id: z.string().describe("The transfer ID"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      id,
      mnemonic,
      network,
      output,
    }: {
      id: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) => handleGetTransfer(id, mnemonic, makeResolve(network), output),
  );
  server.tool(
    "spark_list_transfers",
    "List the most recent transfers (up to 10)",
    {
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      mnemonic,
      network,
      output,
    }: {
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) => handleListTransfers(mnemonic, makeResolve(network), output),
  );

  // Lightning tools
  server.tool(
    "spark_create_invoice",
    "Create a Lightning BOLT11 invoice to receive payment",
    {
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("Amount to receive in satoshis"),
      memo: z.string().optional().describe("Optional payment description"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      amountSats,
      memo,
      mnemonic,
      network,
      output,
    }: {
      amountSats: number;
      memo?: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleCreateInvoice(
        amountSats,
        memo,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );
  server.tool(
    "spark_pay_invoice",
    "Pay a Lightning BOLT11 invoice",
    {
      invoice: z.string().describe("The BOLT11 invoice string"),
      maxFeeSats: z
        .number()
        .int()
        .nonnegative()
        .describe("Maximum fee to pay in satoshis"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      invoice,
      maxFeeSats,
      mnemonic,
      network,
      output,
    }: {
      invoice: string;
      maxFeeSats: number;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handlePayInvoice(
        invoice,
        maxFeeSats,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );
  server.tool(
    "spark_get_lightning_fee_estimate",
    "Estimate the fee for paying a Lightning invoice before committing",
    {
      invoice: z.string().describe("The BOLT11 invoice string"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      invoice,
      mnemonic,
      network,
      output,
    }: {
      invoice: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleGetLightningFeeEstimate(
        invoice,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );

  // Withdrawal tools
  server.tool(
    "spark_get_withdrawal_fee_quote",
    "Get a fee quote for withdrawing funds to a Bitcoin L1 address",
    {
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("Amount to withdraw in satoshis"),
      withdrawalAddress: z
        .string()
        .describe("The Bitcoin address to withdraw to"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      amountSats,
      withdrawalAddress,
      mnemonic,
      network,
      output,
    }: {
      amountSats: number;
      withdrawalAddress: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleGetWithdrawalFeeQuote(
        amountSats,
        withdrawalAddress,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );
  server.tool(
    "spark_withdraw",
    "Withdraw funds from Spark to a Bitcoin L1 address via cooperative exit",
    {
      onchainAddress: z.string().describe("The Bitcoin address to withdraw to"),
      exitSpeed: z
        .enum(["FAST", "MEDIUM", "SLOW"])
        .describe("FAST costs more but settles sooner"),
      amountSats: z
        .number()
        .int()
        .positive()
        .optional()
        .describe("Amount to withdraw (omit to withdraw all)"),
      feeQuoteId: z
        .string()
        .optional()
        .describe("Fee quote ID from spark_get_withdrawal_fee_quote"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      onchainAddress,
      exitSpeed,
      amountSats,
      feeQuoteId,
      mnemonic,
      network,
      output,
    }: {
      onchainAddress: string;
      exitSpeed: "FAST" | "MEDIUM" | "SLOW";
      amountSats?: number;
      feeQuoteId?: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleWithdraw(
        onchainAddress,
        exitSpeed,
        amountSats,
        feeQuoteId,
        mnemonic,
        makeResolve(network),
        output,
      ),
  );
}
