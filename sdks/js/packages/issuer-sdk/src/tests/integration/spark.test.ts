import {
  ConfigOptions,
  filterTokenBalanceForTokenIdentifier,
  RPCError,
  WalletConfig,
} from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "@buildonspark/spark-sdk/test-utils";
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

const brokenTestFn = process.env.GITHUB_ACTIONS ? it.skip : it;

describe.each(TEST_CONFIGS)(
  "token integration tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should create a token", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const tokenName = `${name}Creatable`;
      const tokenTicker = "CRT";
      const maxSupply = 5000n;
      const decimals = 0;
      const txId = await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals,
        isFreezable: false,
        maxSupply,
      });

      expect(typeof txId).toBe("string");
      expect(txId.length).toBeGreaterThan(0);

      const metadata = await issuerWallet.getIssuerTokenMetadata();
      expect(metadata.tokenName).toEqual(tokenName);
      expect(metadata.tokenTicker).toEqual(tokenTicker);
      expect(metadata.maxSupply).toEqual(maxSupply);
      expect(metadata.decimals).toEqual(decimals);
    });

    it("should fail on duplicate token creation", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const tokenName = `${name}Dup`;
      const tokenTicker = `DP${name}`;

      await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals: 0,
        isFreezable: false,
        maxSupply: 100n,
      });

      await expect(
        issuerWallet.createToken({
          tokenName,
          tokenTicker,
          decimals: 0,
          isFreezable: false,
          maxSupply: 100n,
        }),
      ).rejects.toThrow();
    });

    it("should fail when minting tokens without creation", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: config,
      });

      await expect(wallet.mintTokens(tokenAmount)).rejects.toThrow();
    });

    it("should create, and fail when minting more than max supply", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: config,
      });

      await wallet.createToken({
        tokenName: "MST",
        tokenTicker: "MST",
        decimals: 0,
        isFreezable: false,
        maxSupply: 2n,
      });
      await expect(wallet.mintTokens(tokenAmount)).rejects.toThrow();
    });

    it("should create, and mint tokens successfully", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}M`,
        tokenTicker: "MIN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const tokenMetadata = await issuerWallet.getIssuerTokenMetadata();

      const identityPublicKey = await issuerWallet.getIdentityPublicKey();
      expect(tokenMetadata?.tokenName).toEqual(`${name}M`);
      expect(tokenMetadata?.tokenTicker).toEqual("MIN");
      expect(tokenMetadata?.decimals).toEqual(0);
      expect(tokenMetadata?.maxSupply).toEqual(1000000n);
      expect(tokenMetadata?.isFreezable).toEqual(false);

      // Compare the public key using bytesToHex
      const metadataPubkey = tokenMetadata?.tokenPublicKey;
      expect(metadataPubkey).toEqual(identityPublicKey);

      await issuerWallet.mintTokens(tokenAmount);

      const tokenBalance = await issuerWallet.getIssuerTokenBalance();
      expect(tokenBalance.balance).toBeGreaterThanOrEqual(tokenAmount);
    });

    it("should create, mint, and transfer tokens", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });
      await issuerWallet.createToken({
        tokenName: `${name}MTR`,
        tokenTicker: "MTR",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(tokenAmount);

      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await userWallet.getSparkAddress(),
      });

      const balanceObj = await userWallet.getBalance();
      const userBalance = filterTokenBalanceForTokenIdentifier(
        balanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalance.balance).toBeGreaterThanOrEqual(tokenAmount);
    });

    // const tv1It = name.startsWith("TV1") ? it.skip : it.skip;
    // TODO: (CNT-493) Re-enable invoice functionality once spark address migration is complete
    const skipInvoiceTest = it.skip;
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
        expect(satsTransactionSuccess.length).toBe(3); // one sats success per invoice
        expect(satsTransactionErrors.length).toBe(0);
        expect(tokenTransactionSuccess.length).toBe(2); // two token assets - divided into two token transactions
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

    it("should create, mint, and batchtransfer tokens", async () => {
      const tokenAmount: bigint = 999n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}MBN`,
        tokenTicker: "MBN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      const { wallet: destinationWallet2 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet3 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.mintTokens(tokenAmount);
      const sharedIssuerBalance = await issuerWallet.getIssuerTokenBalance();
      expect(sharedIssuerBalance).toBeDefined();
      expect(sharedIssuerBalance.tokenIdentifier).toBeDefined();

      const tokenIdentifier = sharedIssuerBalance.tokenIdentifier!;
      const sourceBalanceBefore = sharedIssuerBalance.balance;

      await issuerWallet.batchTransferTokens([
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet.getSparkAddress(),
        },
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet2.getSparkAddress(),
        },
        {
          tokenAmount: tokenAmount / 3n,
          tokenIdentifier,
          receiverSparkAddress: await destinationWallet3.getSparkAddress(),
        },
      ]);

      const sourceBalanceAfter = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(sourceBalanceAfter).toEqual(sourceBalanceBefore - tokenAmount);

      const balanceObj = await destinationWallet.getBalance();
      const destinationBalance = filterTokenBalanceForTokenIdentifier(
        balanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance.balance).toEqual(tokenAmount / 3n);
      const balanceObj2 = await destinationWallet2.getBalance();
      const destinationBalance2 = filterTokenBalanceForTokenIdentifier(
        balanceObj2?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance2.balance).toEqual(tokenAmount / 3n);
      const balanceObj3 = await destinationWallet3.getBalance();
      const destinationBalance3 = filterTokenBalanceForTokenIdentifier(
        balanceObj3?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance3.balance).toEqual(tokenAmount / 3n);
    });

    it("should track token operations in monitoring", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}FRZ`,
        tokenTicker: "FRZ",
        decimals: 0,
        isFreezable: true,
        maxSupply: 100000n,
      });
      await issuerWallet.mintTokens(tokenAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      const issuerPublicKey = await issuerWallet.getIdentityPublicKey();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await userWallet.getSparkAddress(),
      });

      const userBalanceObj = await userWallet.getBalance();
      const userBalance = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalance.balance).toBeGreaterThanOrEqual(tokenAmount);

      const response = await issuerWallet.queryTokenTransactions({
        tokenIdentifiers: [tokenIdentifier!],
        ownerPublicKeys: [issuerPublicKey],
      });
      const transactions = response.tokenTransactionsWithStatus;
      expect(transactions.length).toBeGreaterThanOrEqual(2);

      let mint_operation = 0;
      let transfer_operation = 0;
      transactions.forEach((transaction) => {
        if (transaction.tokenTransaction?.tokenInputs?.$case === "mintInput") {
          mint_operation++;
        } else if (
          transaction.tokenTransaction?.tokenInputs?.$case === "transferInput"
        ) {
          transfer_operation++;
        }
      });
      expect(mint_operation).toBeGreaterThanOrEqual(1);
      expect(transfer_operation).toBeGreaterThanOrEqual(1);
    });

    it("should correctly assign operation types for complete token lifecycle operations", async () => {
      const tokenAmount = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}LFC`,
        tokenTicker: "LFC",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(tokenAmount);

      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      const issuerPublicKey = await issuerWallet.getIdentityPublicKey();

      await issuerWallet.transferTokens({
        tokenAmount: 500n,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await userWallet.getSparkAddress(),
      });

      await userWallet.transferTokens({
        tokenAmount: 250n,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await issuerWallet.getSparkAddress(),
      });

      const BURN_ADDRESS = "02".repeat(33);

      await issuerWallet.burnTokens(250n);

      const res = await issuerWallet.queryTokenTransactions({
        tokenIdentifiers: [tokenIdentifier!],
        ownerPublicKeys: [issuerPublicKey],
      });
      const transactions = res.tokenTransactionsWithStatus;

      const mintTransaction = transactions.find(
        (tx) => tx.tokenTransaction?.tokenInputs?.$case === "mintInput",
      );

      const transferTransaction = transactions.find(
        (tx) => tx.tokenTransaction?.tokenInputs?.$case === "transferInput",
      );

      const burnTransaction = transactions.find(
        (tx) =>
          tx.tokenTransaction?.tokenInputs?.$case === "transferInput" &&
          bytesToHex(tx.tokenTransaction?.tokenOutputs?.[0]?.ownerPublicKey) ===
            BURN_ADDRESS,
      );

      expect(mintTransaction).toBeDefined();
      expect(transferTransaction).toBeDefined();
      expect(burnTransaction).toBeDefined();
    });

    it("should create, mint, get all transactions, transfer tokens multiple times, get all transactions again, and check difference", async () => {
      const tokenAmount: bigint = 100n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}Transfer`,
        tokenTicker: "TTO",
        decimals: 0,
        isFreezable: false,
        maxSupply: 100000n,
      });

      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();

      await issuerWallet.mintTokens(tokenAmount);

      {
        const res = await issuerWallet.queryTokenTransactions({
          tokenIdentifiers: [tokenIdentifier!],
        });
        const transactions = res.tokenTransactionsWithStatus;
        const amount_of_transactions = transactions.length;
        expect(amount_of_transactions).toEqual(1);
      }

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: await userWallet.getSparkAddress(),
      });

      {
        const res = await issuerWallet.queryTokenTransactions({
          tokenIdentifiers: [tokenIdentifier!],
        });
        const transactions = res.tokenTransactionsWithStatus;
        const amount_of_transactions = transactions.length;
        expect(amount_of_transactions).toEqual(2);
      }

      for (let index = 0; index < 100; ++index) {
        await issuerWallet.mintTokens(tokenAmount);
        await issuerWallet.transferTokens({
          tokenAmount,
          tokenIdentifier: tokenIdentifier!,
          receiverSparkAddress: await userWallet.getSparkAddress(),
        });
      } // 202 in total

      {
        const res = await issuerWallet.queryTokenTransactions({
          tokenIdentifiers: [tokenIdentifier!],
          pageSize: 10,
        });
        const transactions = res.tokenTransactionsWithStatus;
        const amount_of_transactions = transactions.length;
        expect(amount_of_transactions).toEqual(10);
      }

      {
        let hashset_of_all_transactions: Set<String> = new Set();

        let pageSize = 10;
        let offset = 0;
        let page_num = 0;

        while (true) {
          const res = await issuerWallet.queryTokenTransactions({
            tokenIdentifiers: [tokenIdentifier!],
            pageSize,
            offset,
          });
          const transactions = res.tokenTransactionsWithStatus;

          if (transactions.length === 0) {
            break;
          }

          if (offset === 0) {
            expect(transactions.length).toEqual(pageSize);
          }

          for (let index = 0; index < transactions.length; ++index) {
            const element = transactions[index];
            if (element.tokenTransaction !== undefined) {
              const hash: String = bytesToHex(element.tokenTransactionHash);
              if (hashset_of_all_transactions.has(hash)) {
                expect(
                  `Duplicate found. Pagination is broken? Index of transaction: ${index} ; page №: ${page_num} ; page size: ${pageSize} ; hash_duplicate: ${hash}`,
                ).toEqual("");
              } else {
                hashset_of_all_transactions.add(hash);
              }
            } else {
              expect(
                `Transaction is undefined. Something is really wrong. Index of transaction: ${index} ; page №: ${page_num} ; page size: ${pageSize}`,
              ).toEqual("");
            }
          }

          // Prepare for next iteration.
          offset += transactions.length;
          page_num += 1;
        }

        expect(hashset_of_all_transactions.size).toEqual(202);
      }
    });

    it("should mint token with 1 max supply without issue", async () => {
      const tokenAmount: bigint = 1n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "MST",
        tokenTicker: "MST",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
      await issuerWallet.mintTokens(tokenAmount);

      const tokenBalance = await issuerWallet.getIssuerTokenBalance();
      expect(tokenBalance.balance).toEqual(tokenAmount);
    });

    it("should be able to create a token with name of size equal to MAX_SYMBOL_SIZE", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "MST",
        tokenTicker: "TESTAA",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
    });

    it("should be able to create a token with symbol of size equal to MAX_NAME_SIZE", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "ABCDEFGHIJKLMNOPQ",
        tokenTicker: "MQS",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
    });

    it("should create, mint, freeze, and unfreeze tokens", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: `${name}FRZ`,
        tokenTicker: "FRZ",
        decimals: 0,
        isFreezable: true,
        maxSupply: 100000n,
      });
      await issuerWallet.mintTokens(tokenAmount);

      // Check issuer balance after minting
      const issuerBalanceObjAfterMint =
        await issuerWallet.getIssuerTokenBalance();
      expect(issuerBalanceObjAfterMint).toBeDefined();
      expect(issuerBalanceObjAfterMint.tokenIdentifier).toBeDefined();

      const issuerBalanceAfterMint = issuerBalanceObjAfterMint.balance;
      const tokenIdentifier = issuerBalanceObjAfterMint.tokenIdentifier!;

      expect(issuerBalanceAfterMint).toEqual(tokenAmount);

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });
      const userSparkAddress = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier,
        receiverSparkAddress: userSparkAddress,
      });
      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(0n);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      // Freeze tokens
      const freezeResponse = await issuerWallet.freezeTokens(userSparkAddress);
      expect(freezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(freezeResponse.impactedTokenAmount).toEqual(tokenAmount);

      // Unfreeze tokens
      const unfreezeResponse =
        await issuerWallet.unfreezeTokens(userSparkAddress);
      expect(unfreezeResponse.impactedOutputIds.length).toBeGreaterThan(0);
      expect(unfreezeResponse.impactedTokenAmount).toEqual(tokenAmount);
    });

    it("should create, mint and burn tokens", async () => {
      const tokenAmount: bigint = 200n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}MBN`,
        tokenTicker: "MBN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await issuerWallet.mintTokens(tokenAmount);
      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toBeGreaterThanOrEqual(tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(
        issuerTokenBalance - tokenAmount,
      );
    });

    it("should complete a full token lifecycle - create, mint, transfer, return, burn", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}LFC`,
        tokenTicker: "LFC",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      const initialBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;

      await issuerWallet.mintTokens(tokenAmount);
      const issuerBalanceObjAfterMint =
        await issuerWallet.getIssuerTokenBalance();
      expect(issuerBalanceObjAfterMint).toBeDefined();
      const issuerBalanceAfterMint = issuerBalanceObjAfterMint.balance;
      expect(issuerBalanceAfterMint).toEqual(initialBalance + tokenAmount);
      expect(issuerBalanceObjAfterMint.tokenIdentifier).toBeDefined();
      const tokenIdentifier = issuerBalanceObjAfterMint.tokenIdentifier!;
      const userSparkAddress = await userWallet.getSparkAddress();

      await issuerWallet.transferTokens({
        tokenAmount,
        tokenIdentifier,
        receiverSparkAddress: userSparkAddress,
      });

      const issuerBalanceAfterTransfer = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerBalanceAfterTransfer).toEqual(initialBalance);

      const userBalanceObj = await userWallet.getBalance();
      const userBalanceAfterTransfer = filterTokenBalanceForTokenIdentifier(
        userBalanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(userBalanceAfterTransfer.balance).toEqual(tokenAmount);

      await userWallet.transferTokens({
        tokenIdentifier,
        tokenAmount,
        receiverSparkAddress: await issuerWallet.getSparkAddress(),
      });

      const userBalanceObjAfterTransferBack = await userWallet.getBalance();
      const userBalanceAfterTransferBack = filterTokenBalanceForTokenIdentifier(
        userBalanceObjAfterTransferBack?.tokenBalances,
        tokenIdentifier!,
      );

      expect(userBalanceAfterTransferBack.balance).toEqual(0n);

      const issuerTokenBalance = (await issuerWallet.getIssuerTokenBalance())
        .balance;
      expect(issuerTokenBalance).toEqual(initialBalance + tokenAmount);

      await issuerWallet.burnTokens(tokenAmount);

      const issuerTokenBalanceAfterBurn = (
        await issuerWallet.getIssuerTokenBalance()
      ).balance;
      expect(issuerTokenBalanceAfterBurn).toEqual(initialBalance);
    });
  },
);
