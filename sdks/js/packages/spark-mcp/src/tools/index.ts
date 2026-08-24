import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import { outputParam, type OutputMode } from "../utils.js";
import { resolveWallet, createFreshWallet } from "../wallet.js";
import type { ServerConfig } from "../config.js";
import {
  handleGetBalance,
  handleGetSparkAddress,
  handleDisconnectWallet,
} from "./wallet.js";
import { handleGetDepositAddress, handleClaimDeposit } from "./deposits.js";
import {
  handleSendTransfer,
  handleSendMultiTransfer,
  handleGetTransfer,
  handleListTransfers,
} from "./transfers.js";
import {
  handleCreateInvoiceFromQuote,
  handleCreateQuotedInvoice,
  handleLightningReceiveQuote,
} from "./receive-quote.js";
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

// The SSP is asked for a quote before anything parses this key, so an
// unconstrained string spends a round trip to learn it was never a key.
const receiverPubkeyParam = z
  .string()
  .regex(/^0[23][0-9a-fA-F]{64}$/, "must be a 33-byte compressed public key")
  .optional();

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
      handleCreateWallet({ createFresh: makeCreateFresh(network), output }),
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
    }) => handleGetBalance({ mnemonic, resolve: makeResolve(network), output }),
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
    }) =>
      handleGetSparkAddress({
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );
  server.tool(
    "spark_disconnect_wallet",
    "Disconnect a cached wallet, stopping its background stream and closing connections. " +
      "After disconnecting, the wallet will NOT auto-claim incoming transfers until the next tool call re-initializes it. " +
      "Useful in testing to ensure a wallet has not claimed transfers.",
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
    }) =>
      handleDisconnectWallet({
        mnemonic,
        networkOverride: network as "LOCAL" | "REGTEST" | "MAINNET" | undefined,
        output,
      }),
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
    }) =>
      handleGetDepositAddress({
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
      handleClaimDeposit({
        txid,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
        handleFundAddress({
          address,
          amountSats,
          blocksToMine,
          networkOverride: network,
          output,
        }),
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
        handleDeposit({
          amountSats,
          mnemonic,
          networkOverride: network,
          resolve: makeResolve(network),
          output,
        }),
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
      handleSendTransfer({
        receiverSparkAddress,
        amountSats,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );
  server.tool(
    "spark_send_multi_transfer",
    "Send sats to multiple Spark addresses in a single atomic transfer",
    {
      receivers: z
        .array(
          z.object({
            receiverSparkAddress: z
              .string()
              .describe("The recipient's Spark address"),
            amountSats: z
              .number()
              .int()
              .positive()
              .describe("Amount to send to this receiver in satoshis"),
          }),
        )
        .min(1)
        .describe("Receivers with their Spark addresses and amounts"),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      receivers,
      mnemonic,
      network,
      output,
    }: {
      receivers: Array<{ receiverSparkAddress: string; amountSats: number }>;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleSendMultiTransfer({
        receivers,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
    }) =>
      handleGetTransfer({
        id,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
    }) =>
      handleListTransfers({ mnemonic, resolve: makeResolve(network), output }),
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
      handleCreateInvoice({
        amountSats,
        memo,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
      handlePayInvoice({
        invoice,
        maxFeeSats,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
      handleGetLightningFeeEstimate({
        invoice,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );

  // Quoted receive tools. Both the split and one-shot forms ship: an agent
  // almost always wants the one-shot, but reusing a quote must be refused, and
  // only the split form can attempt that.
  server.tool(
    "spark_lightning_receive_quote",
    "Request a Lightning receive fee quote from the SSP. Returns the signed manifest (echo it back verbatim), the issuer signature, the fee breakdown and the attribution status. Does NOT create an invoice — pass the result to spark_create_invoice_from_quote, or use spark_create_quoted_invoice to do both at once.",
    {
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("Amount in satoshis, interpreted per amountBasis"),
      amountBasis: z
        .enum(["NET", "GROSS"])
        .optional()
        .describe(
          "Whether amountSats is the receiver's net (default) or the invoice total. GROSS requires an SSP schema exposing amount_basis.",
        ),
      partnerJwt: z
        .string()
        .optional()
        .describe(
          "Partner JWT to attribute the quote to. Without one the quote comes back feeless and attributionStatus says why.",
        ),
      receiverIdentityPubkey: receiverPubkeyParam.describe(
        "Hex identity public key of the wallet to pay. Defaults to this wallet. Naming another quotes a delegated receive: this wallet still attests, so the payee need not be online.",
      ),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      amountSats,
      amountBasis,
      partnerJwt,
      receiverIdentityPubkey,
      mnemonic,
      network,
      output,
    }: {
      amountSats: number;
      amountBasis?: string;
      partnerJwt?: string;
      receiverIdentityPubkey?: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleLightningReceiveQuote({
        amountSats,
        amountBasis,
        partnerJwt,
        receiverIdentityPubkey,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );
  server.tool(
    "spark_create_invoice_from_quote",
    "Create the Lightning invoice for a quote from spark_lightning_receive_quote. Pass serializedManifest and issuerSignature back exactly as returned. Reusing the same quote is refused, and quotes expire after a few minutes.",
    {
      serializedManifest: z
        .string()
        .describe("serializedManifest from the quote, verbatim hex"),
      issuerSignature: z
        .string()
        .describe("issuerSignature from the quote, verbatim hex"),
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("The amount the quote was requested for"),
      amountBasis: z
        .enum(["NET", "GROSS"])
        .optional()
        .describe("The basis the quote was requested with"),
      memo: z.string().optional().describe("Optional payment description"),
      receiverIdentityPubkey: receiverPubkeyParam.describe(
        "The same receiver the quote was requested with. The manifest already names the payee, so a mismatch is refused.",
      ),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      serializedManifest,
      issuerSignature,
      amountSats,
      amountBasis,
      memo,
      receiverIdentityPubkey,
      mnemonic,
      network,
      output,
    }: {
      serializedManifest: string;
      issuerSignature: string;
      amountSats: number;
      amountBasis?: string;
      memo?: string;
      receiverIdentityPubkey?: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleCreateInvoiceFromQuote({
        serializedManifest,
        issuerSignature,
        amountSats,
        amountBasis,
        memo,
        receiverIdentityPubkey,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );
  server.tool(
    "spark_create_quoted_invoice",
    [
      "Quote a Lightning receive, sign the manifest and issue the invoice in one step — the usual way to create a fee-quoted invoice.",
      "",
      "This creates the invoice only. PAYING it is the counterparty's job and is out of scope for this server, which stays Spark-scoped — drive your own Lightning node.",
      "If that node is the external lnd on a minikube LOCAL stack, two things will otherwise cost you a round trip: it runs with --lnddir=/data, so a plain lncli dies on a missing /root/.lnd/tls.cert, and payinvoice needs --force to be non-interactive.",
    ].join("\n"),
    {
      amountSats: z
        .number()
        .int()
        .positive()
        .describe("Amount in satoshis, interpreted per amountBasis"),
      amountBasis: z
        .enum(["NET", "GROSS"])
        .optional()
        .describe(
          "Whether amountSats is the receiver's net (default) or the invoice total",
        ),
      memo: z.string().optional().describe("Optional payment description"),
      partnerJwt: z
        .string()
        .optional()
        .describe(
          "Partner JWT to attribute the quote to. Without one the invoice is feeless.",
        ),
      receiverIdentityPubkey: receiverPubkeyParam.describe(
        "Hex identity public key of the wallet to pay. Defaults to this wallet. Naming another quotes a delegated receive: this wallet still attests, so the payee need not be online.",
      ),
      mnemonic: mnemonicParam,
      network: networkParam,
      output: outputParam,
    },
    ({
      amountSats,
      amountBasis,
      memo,
      partnerJwt,
      receiverIdentityPubkey,
      mnemonic,
      network,
      output,
    }: {
      amountSats: number;
      amountBasis?: string;
      memo?: string;
      partnerJwt?: string;
      receiverIdentityPubkey?: string;
      mnemonic?: string;
      network?: string;
      output?: OutputMode;
    }) =>
      handleCreateQuotedInvoice({
        amountSats,
        amountBasis,
        memo,
        partnerJwt,
        receiverIdentityPubkey,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
      handleGetWithdrawalFeeQuote({
        amountSats,
        withdrawalAddress,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
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
      handleWithdraw({
        onchainAddress,
        exitSpeed,
        amountSats,
        feeQuoteId,
        mnemonic,
        resolve: makeResolve(network),
        output,
      }),
  );
}
