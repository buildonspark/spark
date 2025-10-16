import {
  ConfigOptions,
  filterTokenBalanceForTokenIdentifier,
  WalletConfig,
} from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";

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

describe.each(TEST_CONFIGS)(
  "token monitoring tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

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
      }

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

          offset += transactions.length;
          page_num += 1;
        }

        expect(hashset_of_all_transactions.size).toEqual(202);
      }
    });
  },
);
