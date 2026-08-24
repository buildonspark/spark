import { describe, expect, it, jest } from "@jest/globals";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";

const resolvedNetworks: (string | undefined)[] = [];
const freshWalletNetworks: (string | undefined)[] = [];

// The whole export surface, because a factory that re-imports the real module
// resolves back to itself and recurses until the heap dies.
jest.unstable_mockModule("../../wallet.js", () => ({
  resolveWallet: (_mnemonic?: string, _init?: unknown, network?: string) => {
    resolvedNetworks.push(network);
    return Promise.resolve({});
  },
  createFreshWallet: (_init?: unknown, network?: string) => {
    freshWalletNetworks.push(network);
    return Promise.resolve({});
  },
  cleanupAllWallets: () => Promise.resolve(),
  evictWallet: () => Promise.resolve(),
  _resetCacheForTesting: () => {},
}));

// A registration that drops an optional field still type-checks and still
// serves a valid tool call. The handler suites call handlers directly, so this
// is the only coverage of what the registration hands them.
const captured: Record<string, Record<string, unknown>> = {};

const record = (name: string) => (args: Record<string, unknown>) => {
  captured[name] = args;
  return Promise.resolve({ content: [] });
};

jest.unstable_mockModule("../../tools/wallet.js", () => ({
  handleGetBalance: record("handleGetBalance"),
  handleGetSparkAddress: record("handleGetSparkAddress"),
  handleDisconnectWallet: record("handleDisconnectWallet"),
}));

jest.unstable_mockModule("../../tools/create-wallet.js", () => ({
  handleCreateWallet: record("handleCreateWallet"),
}));

jest.unstable_mockModule("../../tools/deposits.js", () => ({
  handleGetDepositAddress: record("handleGetDepositAddress"),
  handleClaimDeposit: record("handleClaimDeposit"),
}));

// deposit-flow.js imports handleFundAddress from here, so this mock reaches it too.
jest.unstable_mockModule("../../tools/funding.js", () => ({
  handleFundAddress: record("handleFundAddress"),
}));

jest.unstable_mockModule("../../tools/deposit-flow.js", () => ({
  handleDeposit: record("handleDeposit"),
}));

jest.unstable_mockModule("../../tools/transfers.js", () => ({
  handleSendTransfer: record("handleSendTransfer"),
  handleSendMultiTransfer: record("handleSendMultiTransfer"),
  handleGetTransfer: record("handleGetTransfer"),
  handleListTransfers: record("handleListTransfers"),
}));

jest.unstable_mockModule("../../tools/lightning.js", () => ({
  handleCreateInvoice: record("handleCreateInvoice"),
  handlePayInvoice: record("handlePayInvoice"),
  handleGetLightningFeeEstimate: record("handleGetLightningFeeEstimate"),
}));

jest.unstable_mockModule("../../tools/receive-quote.js", () => ({
  handleLightningReceiveQuote: record("handleLightningReceiveQuote"),
  handleCreateInvoiceFromQuote: record("handleCreateInvoiceFromQuote"),
  handleCreateQuotedInvoice: record("handleCreateQuotedInvoice"),
}));

jest.unstable_mockModule("../../tools/withdrawals.js", () => ({
  handleGetWithdrawalFeeQuote: record("handleGetWithdrawalFeeQuote"),
  handleWithdraw: record("handleWithdraw"),
}));

const { registerAllTools } = await import("../../tools/index.js");

type ToolRun = (args: Record<string, unknown>) => Promise<unknown>;
type Registration = { shape: z.ZodRawShape; run: ToolRun };

function registerAndCapture(
  defaultNetwork: "REGTEST" | "LOCAL" = "REGTEST",
): Map<string, Registration> {
  const tools = new Map<string, Registration>();
  const server = {
    tool: (name: string, ...rest: unknown[]) => {
      tools.set(name, {
        shape: rest[rest.length - 2] as z.ZodRawShape,
        run: rest[rest.length - 1] as ToolRun,
      });
    },
  } as unknown as McpServer;

  registerAllTools(server, { defaultNetwork });
  return tools;
}

// The MCP SDK hands the callback `parseResult.data`, and zod strips silently,
// so a field missing from the shape never reaches a destructure that names it.
function admittedByInputSchema(
  registration: Registration,
  args: Record<string, unknown>,
): Record<string, unknown> {
  const parsed: unknown = z.object(registration.shape).parse(args);
  return parsed as Record<string, unknown>;
}

// `resolve` is a closure over the per-call network override, so asserting its
// identity proves nothing — drive it and see what it forwards.
async function networkReachedBy(resolve: unknown): Promise<string | undefined> {
  resolvedNetworks.length = 0;
  await (resolve as (m?: string) => Promise<unknown>)(MNEMONIC);
  return resolvedNetworks[0];
}

async function networkReachedByCreateFresh(
  createFresh: unknown,
): Promise<string | undefined> {
  freshWalletNetworks.length = 0;
  await (createFresh as () => Promise<unknown>)();
  return freshWalletNetworks[0];
}

// Distinct per field, so a value arriving under the wrong name is recognizable.
const MEMO = "memo-value";
const JWT = "jwt-value";
const MNEMONIC = "mnemonic value here";
const PAYEE = "02".repeat(33);
const MANIFEST = "aa11";
const SIGNATURE = "bb22";
const TXID = "txid-value";
const TRANSFER_ID = "transfer-id-value";
const INVOICE = "invoice-value";
const SPARK_ADDRESS = "spark-address-value";
const FUND_ADDRESS = "fund-address-value";
const WITHDRAWAL_ADDRESS = "withdrawal-address-value";
const ONCHAIN_ADDRESS = "onchain-address-value";
const FEE_QUOTE_ID = "fee-quote-id-value";
const RECEIVERS = [{ receiverSparkAddress: SPARK_ADDRESS, amountSats: 700 }];

// Which injected slot carries the per-call network for a given tool. Named
// rather than inferred, because dropping the wrong one is exactly the bug.
type NetworkCarrier = "resolve" | "createFresh" | "networkOverride";

type ToolCase = {
  tool: string;
  handler: string;
  localOnly?: boolean;
  args: Record<string, unknown>;
  forwarded: Record<string, unknown>;
  networkVia: NetworkCarrier[];
};

const NETWORK = "MAINNET";

const toolCases: ToolCase[] = [
  {
    tool: "spark_create_wallet",
    handler: "handleCreateWallet",
    args: { network: NETWORK, output: "raw" },
    forwarded: { output: "raw" },
    networkVia: ["createFresh"],
  },
  {
    tool: "spark_get_balance",
    handler: "handleGetBalance",
    args: { mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_get_spark_address",
    handler: "handleGetSparkAddress",
    args: { mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_disconnect_wallet",
    handler: "handleDisconnectWallet",
    args: { mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["networkOverride"],
  },
  {
    tool: "spark_get_deposit_address",
    handler: "handleGetDepositAddress",
    args: { mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_claim_deposit",
    handler: "handleClaimDeposit",
    args: { txid: TXID, mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { txid: TXID, mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_fund_address",
    handler: "handleFundAddress",
    localOnly: true,
    args: {
      address: FUND_ADDRESS,
      amountSats: 12_345,
      blocksToMine: 3,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      address: FUND_ADDRESS,
      amountSats: 12_345,
      blocksToMine: 3,
      output: "raw",
    },
    networkVia: ["networkOverride"],
  },
  {
    tool: "spark_deposit",
    handler: "handleDeposit",
    localOnly: true,
    args: {
      amountSats: 23_456,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: { amountSats: 23_456, mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["networkOverride", "resolve"],
  },
  {
    tool: "spark_send_transfer",
    handler: "handleSendTransfer",
    args: {
      receiverSparkAddress: SPARK_ADDRESS,
      amountSats: 1_000,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      receiverSparkAddress: SPARK_ADDRESS,
      amountSats: 1_000,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_send_multi_transfer",
    handler: "handleSendMultiTransfer",
    args: {
      receivers: RECEIVERS,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: { receivers: RECEIVERS, mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_get_transfer",
    handler: "handleGetTransfer",
    args: {
      id: TRANSFER_ID,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: { id: TRANSFER_ID, mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_list_transfers",
    handler: "handleListTransfers",
    args: { mnemonic: MNEMONIC, network: NETWORK, output: "raw" },
    forwarded: { mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_create_invoice",
    handler: "handleCreateInvoice",
    args: {
      amountSats: 1_000,
      memo: MEMO,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      amountSats: 1_000,
      memo: MEMO,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_pay_invoice",
    handler: "handlePayInvoice",
    args: {
      invoice: INVOICE,
      maxFeeSats: 7,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      invoice: INVOICE,
      maxFeeSats: 7,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_get_lightning_fee_estimate",
    handler: "handleGetLightningFeeEstimate",
    args: {
      invoice: INVOICE,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: { invoice: INVOICE, mnemonic: MNEMONIC, output: "raw" },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_lightning_receive_quote",
    handler: "handleLightningReceiveQuote",
    args: {
      amountSats: 1_000,
      amountBasis: "GROSS",
      partnerJwt: JWT,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      amountSats: 1_000,
      amountBasis: "GROSS",
      partnerJwt: JWT,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_create_invoice_from_quote",
    handler: "handleCreateInvoiceFromQuote",
    args: {
      serializedManifest: MANIFEST,
      issuerSignature: SIGNATURE,
      amountSats: 1_000,
      amountBasis: "NET",
      memo: MEMO,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      serializedManifest: MANIFEST,
      issuerSignature: SIGNATURE,
      amountSats: 1_000,
      amountBasis: "NET",
      memo: MEMO,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_create_quoted_invoice",
    handler: "handleCreateQuotedInvoice",
    args: {
      amountSats: 1_000,
      amountBasis: "NET",
      memo: MEMO,
      partnerJwt: JWT,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      amountSats: 1_000,
      amountBasis: "NET",
      memo: MEMO,
      partnerJwt: JWT,
      receiverIdentityPubkey: PAYEE,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_get_withdrawal_fee_quote",
    handler: "handleGetWithdrawalFeeQuote",
    args: {
      amountSats: 1_000,
      withdrawalAddress: WITHDRAWAL_ADDRESS,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      amountSats: 1_000,
      withdrawalAddress: WITHDRAWAL_ADDRESS,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
  {
    tool: "spark_withdraw",
    handler: "handleWithdraw",
    args: {
      onchainAddress: ONCHAIN_ADDRESS,
      exitSpeed: "FAST",
      amountSats: 4_242,
      feeQuoteId: FEE_QUOTE_ID,
      mnemonic: MNEMONIC,
      network: NETWORK,
      output: "raw",
    },
    forwarded: {
      onchainAddress: ONCHAIN_ADDRESS,
      exitSpeed: "FAST",
      amountSats: 4_242,
      feeQuoteId: FEE_QUOTE_ID,
      mnemonic: MNEMONIC,
      output: "raw",
    },
    networkVia: ["resolve"],
  },
];

describe("tool registrations", () => {
  it("registers every tool the table covers, and no others", () => {
    const registered = [...registerAndCapture("LOCAL").keys()].sort();
    expect(registered).toEqual(toolCases.map((c) => c.tool).sort());
  });

  it("gates the funding tools on a LOCAL default network", () => {
    const regtest = [...registerAndCapture("REGTEST").keys()];
    expect(regtest).not.toContain("spark_fund_address");
    expect(regtest).not.toContain("spark_deposit");
  });

  for (const testCase of toolCases) {
    it(`declares every ${testCase.tool} argument in its input schema`, () => {
      const tools = registerAndCapture(
        testCase.localOnly ? "LOCAL" : "REGTEST",
      );
      const registration = tools.get(testCase.tool);
      expect(registration).toBeDefined();

      const admitted = admittedByInputSchema(registration!, testCase.args);
      expect(Object.keys(admitted).sort()).toEqual(
        Object.keys(testCase.args).sort(),
      );
      // The parse can only reveal a field the schema strips. Compare the other
      // direction too, or a field added to the schema alone goes undriven.
      expect(Object.keys(registration!.shape).sort()).toEqual(
        Object.keys(testCase.args).sort(),
      );
    });

    it(`hands ${testCase.tool} its arguments under the right names`, async () => {
      const tools = registerAndCapture(
        testCase.localOnly ? "LOCAL" : "REGTEST",
      );
      const registration = tools.get(testCase.tool);
      expect(registration).toBeDefined();
      await registration!.run(
        admittedByInputSchema(registration!, testCase.args),
      );

      const args = captured[testCase.handler];
      for (const [field, expected] of Object.entries(testCase.forwarded)) {
        // Wrapped so a failure names the field rather than a loop iteration.
        expect({ [field]: args[field] }).toEqual({ [field]: expected });
      }

      for (const carrier of testCase.networkVia) {
        if (carrier === "resolve") {
          expect(await networkReachedBy(args["resolve"])).toBe(NETWORK);
        } else if (carrier === "createFresh") {
          expect(await networkReachedByCreateFresh(args["createFresh"])).toBe(
            NETWORK,
          );
        } else {
          expect(args["networkOverride"]).toBe(NETWORK);
        }
      }
    });
  }
});
