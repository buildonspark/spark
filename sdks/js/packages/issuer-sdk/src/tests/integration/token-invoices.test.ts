import {
  ConfigOptions,
  filterTokenBalanceForTokenIdentifier,
  RPCError,
  WalletConfig,
} from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import {
  BitcoinFaucet,
  SparkWalletTesting,
} from "@buildonspark/spark-sdk/test-utils";
import { InvoiceStatus } from "@buildonspark/spark-sdk/proto/spark";

export const TOKENS_SCHNORR_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
};

export const TOKENS_ECDSA_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
};

const TEST_CONFIGS = [
  { name: "TE", config: TOKENS_ECDSA_CONFIG },
  { name: "TS", config: TOKENS_SCHNORR_CONFIG },
];

const skipInvoiceTest = it.skip;

describe.each(TEST_CONFIGS)(
  "token invoice tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    skipInvoiceTest("should transfer tokens using spark invoices", async () => {
      const tokenAmount: bigint = 777n;
      const initialIssuerBalance = 100000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}INV`,
        tokenTicker: "INV",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(initialIssuerBalance);

      const issuerBalanceAfterMint = await issuerWallet.getIssuerTokenBalance();
      expect(issuerBalanceAfterMint).toBeDefined();
      expect(issuerBalanceAfterMint.balance).toBe(initialIssuerBalance);
      const tokenIdentifier = issuerBalanceAfterMint.tokenIdentifier!;
      const issuerBalanceBeforeTransfer = issuerBalanceAfterMint.balance;

      const invoice = await receiverWallet.createTokensInvoice({
        amount: tokenAmount,
        tokenIdentifier,
        memo: "Invoice test",
        expiryTime: new Date(Date.now() + 1000 * 60 * 60 * 24),
      });

      const { tokenTransactionSuccess } =
        await issuerWallet.fulfillSparkInvoice([{ invoice }]);
      expect(tokenTransactionSuccess.length).toBe(1);
      expect(tokenTransactionSuccess[0].txid).toBeDefined();
      expect(tokenTransactionSuccess[0].txid.length).toBeGreaterThan(0);

      const issuerBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerBalanceAfter).toEqual(
        issuerBalanceBeforeTransfer - tokenAmount,
      );

      const receiverBalanceObj = await receiverWallet.getBalance();
      const receiverBalance = filterTokenBalanceForTokenIdentifier(
        receiverBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(receiverBalance.balance).toEqual(tokenAmount);
    });

    skipInvoiceTest(
      "should transfer tokens using multiple spark invoices",
      async () => {
        const amount1: bigint = 111n;
        const amount2: bigint = 222n;
        const amount3: bigint = 333n;
        const totalAmount: bigint = amount1 + amount2 + amount3;
        const initialIssuerBalance = 100000n;

        const { wallet: issuerWallet } =
          await IssuerSparkWalletTesting.initialize({
            options: config,
          });
        const { wallet: receiverWallet1 } = await SparkWalletTesting.initialize(
          {
            options: config,
          },
        );
        const { wallet: receiverWallet2 } = await SparkWalletTesting.initialize(
          {
            options: config,
          },
        );

        await issuerWallet.createToken({
          tokenName: `${name}INVM`,
          tokenTicker: "INM",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });

        await issuerWallet.mintTokens(initialIssuerBalance);

        const issuerBalanceAfterMint =
          await issuerWallet.getIssuerTokenBalance();
        expect(issuerBalanceAfterMint).toBeDefined();
        expect(issuerBalanceAfterMint.balance).toBe(initialIssuerBalance);
        const tokenIdentifier = issuerBalanceAfterMint.tokenIdentifier!;
        const issuerBalanceBeforeTransfer = issuerBalanceAfterMint.balance;

        const invoice1 = await receiverWallet1.createTokensInvoice({
          amount: amount1,
          tokenIdentifier,
          memo: "Invoice #1",
          expiryTime: new Date(Date.now() + 1000 * 60 * 60 * 24),
        });

        const invoice2 = await receiverWallet1.createTokensInvoice({
          amount: amount2,
          tokenIdentifier,
          memo: "Invoice #2",
          expiryTime: new Date(Date.now() + 1000 * 60 * 60 * 24),
        });

        const invoice3 = await receiverWallet2.createTokensInvoice({
          amount: amount3,
          tokenIdentifier,
          memo: "Invoice #3",
          expiryTime: new Date(Date.now() + 1000 * 60 * 60 * 24),
        });

        const { tokenTransactionSuccess } =
          await issuerWallet.fulfillSparkInvoice([
            { invoice: invoice1 },
            { invoice: invoice2 },
            { invoice: invoice3 },
          ]);
        expect(tokenTransactionSuccess.length).toBe(1);
        expect(tokenTransactionSuccess[0].txid).toBeDefined();
        expect(tokenTransactionSuccess[0].txid.length).toBeGreaterThan(0);

        const issuerBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
          .balance;
        expect(issuerBalanceAfter).toEqual(
          issuerBalanceBeforeTransfer - totalAmount,
        );

        const receiver1BalanceObj = await receiverWallet1.getBalance();
        const receiver1Balance = filterTokenBalanceForTokenIdentifier(
          receiver1BalanceObj?.tokenBalances,
          tokenIdentifier!,
        );
        expect(receiver1Balance.balance).toEqual(amount1 + amount2);

        const receiver2BalanceObj = await receiverWallet2.getBalance();
        const receiver2Balance = filterTokenBalanceForTokenIdentifier(
          receiver2BalanceObj?.tokenBalances,
          tokenIdentifier!,
        );
        expect(receiver2Balance.balance).toEqual(amount3);
      },
    );

    skipInvoiceTest(
      "should fail to fulfill an expired spark invoice",
      async () => {
        const tokenAmount: bigint = 123n;
        const initialIssuerBalance = 100000n;

        const { wallet: issuerWallet } =
          await IssuerSparkWalletTesting.initialize({
            options: config,
          });
        const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
          options: config,
        });

        await issuerWallet.createToken({
          tokenName: `${name}INVEXP`,
          tokenTicker: "INVX",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });

        await issuerWallet.mintTokens(initialIssuerBalance);

        const issuerBalanceAfterMint =
          await issuerWallet.getIssuerTokenBalance();
        expect(issuerBalanceAfterMint).toBeDefined();
        expect(issuerBalanceAfterMint.balance).toBe(initialIssuerBalance);
        const tokenIdentifier = issuerBalanceAfterMint.tokenIdentifier!;
        const issuerBalanceBefore = issuerBalanceAfterMint.balance;

        const expiredInvoice = await receiverWallet.createTokensInvoice({
          amount: tokenAmount,
          tokenIdentifier,
          memo: "Expired invoice",
          expiryTime: new Date(Date.now() - 60_000),
        });

        const { invalidInvoices } = await issuerWallet.fulfillSparkInvoice([
          { invoice: expiredInvoice },
        ]);
        expect(invalidInvoices.length).toBe(1);
        expect(invalidInvoices[0].invoice).toBe(expiredInvoice);

        const issuerBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
          .balance;
        expect(issuerBalanceAfter).toEqual(issuerBalanceBefore);

        const receiverBalanceObj = await receiverWallet.getBalance();
        const receiverBalance = filterTokenBalanceForTokenIdentifier(
          receiverBalanceObj?.tokenBalances,
          tokenIdentifier!,
        );
        expect(receiverBalance.balance).toEqual(0n);
      },
    );

    skipInvoiceTest(
      "should fulfill a spark invoice with null expiry",
      async () => {
        const tokenAmount: bigint = 321n;
        const initialIssuerBalance = 100000n;

        const { wallet: issuerWallet } =
          await IssuerSparkWalletTesting.initialize({
            options: config,
          });
        const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
          options: config,
        });

        await issuerWallet.createToken({
          tokenName: `${name}INVNULL`,
          tokenTicker: "INVN",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });

        await issuerWallet.mintTokens(initialIssuerBalance);

        const issuerBalanceAfterMint =
          await issuerWallet.getIssuerTokenBalance();
        expect(issuerBalanceAfterMint).toBeDefined();
        expect(issuerBalanceAfterMint.balance).toBe(initialIssuerBalance);
        const tokenIdentifier = issuerBalanceAfterMint.tokenIdentifier!;
        const issuerBalanceBefore = issuerBalanceAfterMint.balance;

        const nullExpiryInvoice = await receiverWallet.createTokensInvoice({
          amount: tokenAmount,
          tokenIdentifier,
          memo: "Null expiry invoice",
          expiryTime: null as unknown as Date,
        });

        const { tokenTransactionSuccess } =
          await issuerWallet.fulfillSparkInvoice([
            { invoice: nullExpiryInvoice },
          ]);
        expect(tokenTransactionSuccess.length).toBe(1);
        expect(tokenTransactionSuccess[0].txid).toBeDefined();
        expect(tokenTransactionSuccess[0].txid.length).toBeGreaterThan(0);

        const issuerBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
          .balance;
        expect(issuerBalanceAfter).toEqual(issuerBalanceBefore - tokenAmount);

        const receiverBalanceObj = await receiverWallet.getBalance();
        const receiverBalance = filterTokenBalanceForTokenIdentifier(
          receiverBalanceObj?.tokenBalances,
          tokenIdentifier!,
        );
        expect(receiverBalance.balance).toEqual(tokenAmount);
      },
    );

    skipInvoiceTest(
      "should fulfill a tokens invoice without amount by passing amount parameter",
      async () => {
        const tokenAmount: bigint = 555n;
        const initialIssuerBalance = 100000n;

        const { wallet: issuerWallet } =
          await IssuerSparkWalletTesting.initialize({
            options: config,
          });
        const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
          options: config,
        });

        await issuerWallet.createToken({
          tokenName: `${name}INVAOPT`,
          tokenTicker: "INO",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });

        await issuerWallet.mintTokens(initialIssuerBalance);

        const issuerBalanceAfterMint =
          await issuerWallet.getIssuerTokenBalance();
        expect(issuerBalanceAfterMint).toBeDefined();
        expect(issuerBalanceAfterMint.balance).toBe(initialIssuerBalance);
        const tokenIdentifier = issuerBalanceAfterMint.tokenIdentifier!;
        const issuerBalanceBeforeTransfer = issuerBalanceAfterMint.balance;

        const invoiceWithoutAmount = await receiverWallet.createTokensInvoice({
          tokenIdentifier,
          memo: "Invoice without preset amount",
          expiryTime: new Date(Date.now() + 1000 * 60 * 60 * 24),
        });

        const { tokenTransactionSuccess } =
          await issuerWallet.fulfillSparkInvoice([
            { invoice: invoiceWithoutAmount, amount: tokenAmount },
          ]);
        expect(tokenTransactionSuccess.length).toBe(1);
        expect(tokenTransactionSuccess[0].txid).toBeDefined();
        expect(tokenTransactionSuccess[0].txid.length).toBeGreaterThan(0);

        const issuerBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
          .balance;
        expect(issuerBalanceAfter).toEqual(
          issuerBalanceBeforeTransfer - tokenAmount,
        );

        const receiverBalanceObj = await receiverWallet.getBalance();
        const receiverBalance = filterTokenBalanceForTokenIdentifier(
          receiverBalanceObj?.tokenBalances,
          tokenIdentifier!,
        );
        expect(receiverBalance.balance).toEqual(tokenAmount);
      },
    );

    skipInvoiceTest(
      `fulfillSparkInvoice successfully handles multiple mixed tokens and sats invoices`,
      async () => {
        const faucet = BitcoinFaucet.getInstance();
        const { wallet: sdk } = await IssuerSparkWalletTesting.initialize({
          options: config,
        });
        await sdk.createToken({
          tokenName: `SDKONE`,
          tokenTicker: "SDK1",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });
        const { wallet: sdk2 } = await IssuerSparkWalletTesting.initialize({
          options: config,
        });
        await sdk2.createToken({
          tokenName: `SDKTWO`,
          tokenTicker: "SDK2",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1_000_000n,
        });

        await sdk.mintTokens(1_000_000n);
        await sdk2.mintTokens(1_000_000n);

        const sdkOneTokenIdentifier = await sdk.getIssuerTokenIdentifier();
        const sdkTwoTokenIdentifier = await sdk2.getIssuerTokenIdentifier();

        await sdk2.transferTokens({
          tokenAmount: 1_000_000n,
          tokenIdentifier: sdkTwoTokenIdentifier,
          receiverSparkAddress: await sdk.getSparkAddress(),
        });

        const depositAddrOne = await sdk.getSingleUseDepositAddress();
        if (!depositAddrOne) {
          throw new RPCError("Deposit address not found", {
            method: "getDepositAddress",
          });
        }
        const depositAddrTwo = await sdk.getSingleUseDepositAddress();
        if (!depositAddrTwo) {
          throw new RPCError("Deposit address not found", {
            method: "getDepositAddress",
          });
        }
        const depositAddrThree = await sdk.getSingleUseDepositAddress();
        if (!depositAddrThree) {
          throw new RPCError("Deposit address not found", {
            method: "getDepositAddress",
          });
        }

        const oneThousand = await faucet.sendToAddress(depositAddrOne, 1_000n);
        const twoThousand = await faucet.sendToAddress(depositAddrTwo, 2_000n);
        const threeThousand = await faucet.sendToAddress(
          depositAddrThree,
          3_000n,
        );

        await sdk.claimDeposit(oneThousand.id);
        await sdk.claimDeposit(twoThousand.id);
        await sdk.claimDeposit(threeThousand.id);

        const balance = await sdk.getBalance();
        expect(balance.balance).toBe(6_000n);

        const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
        const invoice1000 = await sdk2.createSatsInvoice({
          amount: 1_000,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const invoice2000 = await sdk2.createSatsInvoice({
          amount: 2_000,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const invoiceNilAmount = await sdk2.createSatsInvoice({
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const sdkOneTokenInvoiceA = await sdk2.createTokensInvoice({
          amount: 1_000n,
          tokenIdentifier: sdkOneTokenIdentifier,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const sdkOneTokenInvoiceB = await sdk2.createTokensInvoice({
          amount: 2_000n,
          tokenIdentifier: sdkOneTokenIdentifier,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const sdkTwoTokenInvoiceA = await sdk2.createTokensInvoice({
          amount: 1_000n,
          tokenIdentifier: sdkTwoTokenIdentifier,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });
        const sdkTwoTokenNilAmountInvoiceB = await sdk2.createTokensInvoice({
          tokenIdentifier: sdkTwoTokenIdentifier,
          memo: "Test invoice",
          expiryTime: tomorrow,
        });

        const transferResults = await sdk.fulfillSparkInvoice([
          { invoice: invoice1000 },
          { invoice: invoice2000 },
          { invoice: invoiceNilAmount, amount: 3_000n },
          { invoice: sdkOneTokenInvoiceA },
          { invoice: sdkOneTokenInvoiceB },
          { invoice: sdkTwoTokenInvoiceA },
          { invoice: sdkTwoTokenNilAmountInvoiceB, amount: 3_000n },
        ]);

        const {
          satsTransactionSuccess,
          satsTransactionErrors,
          tokenTransactionSuccess,
          tokenTransactionErrors,
          invalidInvoices,
        } = transferResults;
        expect(satsTransactionSuccess.length).toBe(3);
        expect(satsTransactionErrors.length).toBe(0);
        expect(tokenTransactionSuccess.length).toBe(2);
        expect(tokenTransactionErrors.length).toBe(0);
        expect(invalidInvoices.length).toBe(0);
        const invoicesToQuery = [
          invoice1000,
          invoice2000,
          invoiceNilAmount,
          sdkOneTokenInvoiceA,
          sdkOneTokenInvoiceB,
          sdkTwoTokenInvoiceA,
          sdkTwoTokenNilAmountInvoiceB,
        ];
        const queryInvoiceResponse = await (sdk as any).querySparkInvoices(
          invoicesToQuery,
        );
        expect(queryInvoiceResponse.invoiceStatuses.length).toBe(7);
        for (let i = 0; i < queryInvoiceResponse.invoiceStatuses.length; i++) {
          const response = queryInvoiceResponse.invoiceStatuses[i];
          const invoiceStatus = response.status;
          expect(invoiceStatus).toBeDefined();
          expect(invoiceStatus).toBe(InvoiceStatus.FINALIZED);
          expect(response.invoice).toBe(invoicesToQuery[i]);
        }
      },
    );
  },
);
