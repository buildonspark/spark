import {
  decodeBech32mTokenIdentifier,
  filterTokenBalanceForTokenIdentifier,
} from "@buildonspark/spark-sdk";
import { OutputWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark_token";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";
import { TEST_CONFIGS } from "./test-configs.js";

describe.each(TEST_CONFIGS)(
  "token transfer tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

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
      expect(userBalance.ownedBalance).toBeGreaterThanOrEqual(tokenAmount);
    });

    it("should create, mint, and batch transfer tokens", async () => {
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

      const { wallet: destinationWallet1 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

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
          receiverSparkAddress: await destinationWallet1.getSparkAddress(),
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

      const balanceObj = await destinationWallet1.getBalance();
      const destinationBalance = filterTokenBalanceForTokenIdentifier(
        balanceObj?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance.ownedBalance).toEqual(tokenAmount / 3n);
      const balanceObj2 = await destinationWallet2.getBalance();
      const destinationBalance2 = filterTokenBalanceForTokenIdentifier(
        balanceObj2?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance2.ownedBalance).toEqual(tokenAmount / 3n);
      const balanceObj3 = await destinationWallet3.getBalance();
      const destinationBalance3 = filterTokenBalanceForTokenIdentifier(
        balanceObj3?.tokenBalances,
        tokenIdentifier!,
      );
      expect(destinationBalance3.ownedBalance).toEqual(tokenAmount / 3n);
    });

    it("should create, mint, and batch transfer multiple token types", async () => {
      const tokenConfigs = [
        { amount: 1000n, ticker: "MTT1" },
        { amount: 2000n, ticker: "MTT2" },
        { amount: 1500n, ticker: "MTT3" },
      ];

      const issuerWallets = await Promise.all(
        tokenConfigs.map(async ({ ticker }) => {
          const { wallet } = await IssuerSparkWalletTesting.initialize({
            options: config,
          });
          await wallet.createToken({
            tokenName: `${name}${ticker}`,
            tokenTicker: ticker,
            decimals: 0,
            isFreezable: false,
            maxSupply: 10_000_000n,
          });
          return wallet;
        }),
      );

      const { wallet: intermediateWallet } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const destinationWallets = await Promise.all(
        tokenConfigs.map(() =>
          SparkWalletTesting.initialize({ options: config }).then(
            (r) => r.wallet,
          ),
        ),
      );

      await Promise.all(
        issuerWallets.map((wallet, i) =>
          wallet.mintTokens(tokenConfigs[i].amount),
        ),
      );

      const tokenIdentifiers = await Promise.all(
        issuerWallets.map(async (wallet) => {
          const balance = await wallet.getIssuerTokenBalance();
          expect(balance.tokenIdentifier).toBeDefined();
          return balance.tokenIdentifier!;
        }),
      );

      const intermediateAddress = await intermediateWallet.getSparkAddress();
      for (let i = 0; i < issuerWallets.length; i++) {
        await issuerWallets[i].transferTokens({
          tokenAmount: tokenConfigs[i].amount,
          tokenIdentifier: tokenIdentifiers[i],
          receiverSparkAddress: intermediateAddress,
        });
      }

      const intermediateBalanceObj = await intermediateWallet.getBalance();
      for (let i = 0; i < tokenConfigs.length; i++) {
        const balance = filterTokenBalanceForTokenIdentifier(
          intermediateBalanceObj?.tokenBalances,
          tokenIdentifiers[i],
        );
        expect(balance.ownedBalance).toEqual(tokenConfigs[i].amount);
      }

      await intermediateWallet.batchTransferTokens(
        await Promise.all(
          tokenConfigs.map(async ({ amount }, i) => ({
            tokenAmount: amount,
            tokenIdentifier: tokenIdentifiers[i],
            receiverSparkAddress: await destinationWallets[i].getSparkAddress(),
          })),
        ),
      );

      for (let i = 0; i < destinationWallets.length; i++) {
        const balanceObj = await destinationWallets[i].getBalance();
        const balance = filterTokenBalanceForTokenIdentifier(
          balanceObj?.tokenBalances,
          tokenIdentifiers[i],
        );
        expect(balance.ownedBalance).toEqual(tokenConfigs[i].amount);
      }

      const finalIntermediateBalanceObj = await intermediateWallet.getBalance();
      for (const tokenIdentifier of tokenIdentifiers) {
        const balance = filterTokenBalanceForTokenIdentifier(
          finalIntermediateBalanceObj?.tokenBalances,
          tokenIdentifier,
        );
        expect(balance.ownedBalance).toEqual(0n);
      }
    });

    it("should fail when transferring more tokens than available balance", async () => {
      const mintAmount: bigint = 1000n;
      const transferAmount: bigint = 2000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}INS`,
        tokenTicker: "INS",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: destinationWallet1 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.mintTokens(mintAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();

      await expect(
        issuerWallet.transferTokens({
          tokenAmount: transferAmount,
          tokenIdentifier: tokenIdentifier!,
          receiverSparkAddress: await destinationWallet1.getSparkAddress(),
        }),
      ).rejects.toThrow(/Insufficient token amount/);
    });

    it("should fail when batch transferring more tokens than available balance", async () => {
      const mintAmount: bigint = 1000n;
      const transferAmount: bigint = 2000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}INS`,
        tokenTicker: "INS",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: destinationWallet1 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet2 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.mintTokens(mintAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();

      await expect(
        issuerWallet.batchTransferTokens([
          {
            tokenAmount: transferAmount,
            tokenIdentifier: tokenIdentifier!,
            receiverSparkAddress: await destinationWallet1.getSparkAddress(),
          },
          {
            tokenAmount: transferAmount,
            tokenIdentifier: tokenIdentifier!,
            receiverSparkAddress: await destinationWallet2.getSparkAddress(),
          },
        ]),
      ).rejects.toThrow(/Insufficient token amount/);
    });

    it("should fail batch transfer when one token type has insufficient balance", async () => {
      const tokenAmount1: bigint = 1000n;
      const tokenAmount2: bigint = 500n;

      const { wallet: issuerWallet1 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet1.createToken({
        tokenName: `${name}BF1`,
        tokenTicker: "BF1",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: issuerWallet2 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet2.createToken({
        tokenName: `${name}BF2`,
        tokenTicker: "BF2",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: intermediateWallet } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet1 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet2 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet1.mintTokens(tokenAmount1);
      await issuerWallet2.mintTokens(tokenAmount2);

      const issuerBalance1 = await issuerWallet1.getIssuerTokenBalance();
      const issuerBalance2 = await issuerWallet2.getIssuerTokenBalance();

      const tokenIdentifier1 = issuerBalance1.tokenIdentifier!;
      const tokenIdentifier2 = issuerBalance2.tokenIdentifier!;

      await issuerWallet1.transferTokens({
        tokenAmount: tokenAmount1,
        tokenIdentifier: tokenIdentifier1,
        receiverSparkAddress: await intermediateWallet.getSparkAddress(),
      });

      await issuerWallet2.transferTokens({
        tokenAmount: tokenAmount2,
        tokenIdentifier: tokenIdentifier2,
        receiverSparkAddress: await intermediateWallet.getSparkAddress(),
      });

      await expect(
        intermediateWallet.batchTransferTokens([
          {
            tokenAmount: tokenAmount1,
            tokenIdentifier: tokenIdentifier1,
            receiverSparkAddress: await destinationWallet1.getSparkAddress(),
          },
          {
            tokenAmount: tokenAmount2 * 2n,
            tokenIdentifier: tokenIdentifier2,
            receiverSparkAddress: await destinationWallet2.getSparkAddress(),
          },
        ]),
      ).rejects.toThrow(/Insufficient token amount/);
    });

    it("should fail batch transfer when requesting token type wallet doesn't own", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet1 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet1.createToken({
        tokenName: `${name}OWN`,
        tokenTicker: "OWN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: issuerWallet2 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet2.createToken({
        tokenName: `${name}NWN`,
        tokenTicker: "NWN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: intermediateWallet } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      await issuerWallet1.mintTokens(tokenAmount);
      await issuerWallet2.mintTokens(tokenAmount);

      const issuerBalance1 = await issuerWallet1.getIssuerTokenBalance();
      const issuerBalance2 = await issuerWallet2.getIssuerTokenBalance();

      const tokenIdentifier1 = issuerBalance1.tokenIdentifier!;
      const tokenIdentifier2 = issuerBalance2.tokenIdentifier!;

      await issuerWallet1.transferTokens({
        tokenAmount: tokenAmount,
        tokenIdentifier: tokenIdentifier1,
        receiverSparkAddress: await intermediateWallet.getSparkAddress(),
      });

      await expect(
        intermediateWallet.batchTransferTokens([
          {
            tokenAmount: tokenAmount,
            tokenIdentifier: tokenIdentifier1,
            receiverSparkAddress: await destinationWallet.getSparkAddress(),
          },
          {
            tokenAmount: tokenAmount,
            tokenIdentifier: tokenIdentifier2,
            receiverSparkAddress: await destinationWallet.getSparkAddress(),
          },
        ]),
      ).rejects.toThrow(/Insufficient token amount/);
    });

    it("should fail when transfer amounts sum exceeds balance for same token type", async () => {
      const mintAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}SUM`,
        tokenTicker: "SUM",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: destinationWallet1 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet2 } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.mintTokens(mintAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();

      await expect(
        issuerWallet.batchTransferTokens([
          {
            tokenAmount: 600n,
            tokenIdentifier: tokenIdentifier!,
            receiverSparkAddress: await destinationWallet1.getSparkAddress(),
          },
          {
            tokenAmount: 600n,
            tokenIdentifier: tokenIdentifier!,
            receiverSparkAddress: await destinationWallet2.getSparkAddress(),
          },
        ]),
      ).rejects.toThrow(/Insufficient token amount/);
    });

    it("should fail when transferring with unavailable selected TTXOs", async () => {
      const mintAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}UNA`,
        tokenTicker: "UNA",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      await issuerWallet.mintTokens(mintAmount);
      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      const { tokenIdentifier: rawTokenIdentifier } =
        decodeBech32mTokenIdentifier(tokenIdentifier!);

      const fakeOutput: OutputWithPreviousTransactionData = {
        output: {
          id: "non-existent-output-id",
          ownerPublicKey: new Uint8Array(33),
          tokenIdentifier: rawTokenIdentifier,
          tokenAmount: new Uint8Array([
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x03, 0xe8,
          ]),
        },
        previousTransactionHash: new Uint8Array(32),
        previousTransactionVout: 0,
      };

      await expect(
        issuerWallet.transferTokens({
          tokenAmount: mintAmount,
          tokenIdentifier: tokenIdentifier!,
          receiverSparkAddress: await destinationWallet.getSparkAddress(),
          selectedOutputs: [fakeOutput],
        }),
      ).rejects.toThrow(/Insufficient input amount for token/);
    });

    it("should fail when selected output token type does not match receiver token type", async () => {
      const mintAmount: bigint = 1000n;

      const { wallet: issuerWallet1 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet1.createToken({
        tokenName: `${name}MIS1`,
        tokenTicker: "MIS1",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: issuerWallet2 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet2.createToken({
        tokenName: `${name}MIS2`,
        tokenTicker: "MIS2",
        decimals: 0,
        isFreezable: false,
        maxSupply: 10_000_000n,
      });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      await issuerWallet1.mintTokens(mintAmount);
      await issuerWallet2.mintTokens(mintAmount);

      const tokenIdentifier1 = await issuerWallet1.getIssuerTokenIdentifier();
      const tokenIdentifier2 = await issuerWallet2.getIssuerTokenIdentifier();
      const { tokenIdentifier: rawTokenIdentifier2 } =
        decodeBech32mTokenIdentifier(tokenIdentifier2!);

      const fakeOutputWithWrongToken: OutputWithPreviousTransactionData = {
        output: {
          id: "fake-output-id",
          ownerPublicKey: new Uint8Array(33),
          tokenIdentifier: rawTokenIdentifier2,
          tokenAmount: new Uint8Array([
            0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x03, 0xe8,
          ]),
        },
        previousTransactionHash: new Uint8Array(32),
        previousTransactionVout: 0,
      };

      await expect(
        issuerWallet1.transferTokens({
          tokenAmount: mintAmount,
          tokenIdentifier: tokenIdentifier1!,
          receiverSparkAddress: await destinationWallet.getSparkAddress(),
          selectedOutputs: [fakeOutputWithWrongToken],
        }),
      ).rejects.toThrow(/Insufficient input amount for token/);
    });

    it("should fail batch transfer when one token type is frozen", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet1 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet1.createToken({
        tokenName: `${name}FRZ1`,
        tokenTicker: "FRZ1",
        decimals: 0,
        isFreezable: true,
        maxSupply: 10_000_000n,
      });

      const { wallet: issuerWallet2 } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet2.createToken({
        tokenName: `${name}FRZ2`,
        tokenTicker: "FRZ2",
        decimals: 0,
        isFreezable: true,
        maxSupply: 10_000_000n,
      });

      const { wallet: intermediateWallet } =
        await SparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: destinationWallet } = await SparkWalletTesting.initialize(
        {
          options: config,
        },
      );

      await issuerWallet1.mintTokens(tokenAmount);
      await issuerWallet2.mintTokens(tokenAmount);

      const issuerBalance1 = await issuerWallet1.getIssuerTokenBalance();
      const issuerBalance2 = await issuerWallet2.getIssuerTokenBalance();
      const tokenIdentifier1 = issuerBalance1.tokenIdentifier!;
      const tokenIdentifier2 = issuerBalance2.tokenIdentifier!;

      const intermediateAddress = await intermediateWallet.getSparkAddress();
      await issuerWallet1.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier1,
        receiverSparkAddress: intermediateAddress,
      });
      await issuerWallet2.transferTokens({
        tokenAmount,
        tokenIdentifier: tokenIdentifier2,
        receiverSparkAddress: intermediateAddress,
      });

      const intermediateBalanceObj = await intermediateWallet.getBalance();
      expect(
        filterTokenBalanceForTokenIdentifier(
          intermediateBalanceObj?.tokenBalances,
          tokenIdentifier1,
        ).ownedBalance,
      ).toEqual(tokenAmount);
      expect(
        filterTokenBalanceForTokenIdentifier(
          intermediateBalanceObj?.tokenBalances,
          tokenIdentifier2,
        ).ownedBalance,
      ).toEqual(tokenAmount);

      await issuerWallet1.freezeTokens({
        tokenIdentifier: tokenIdentifier1,
        sparkAddress: intermediateAddress,
      });

      const destinationAddress = await destinationWallet.getSparkAddress();
      await expect(
        intermediateWallet.batchTransferTokens([
          {
            tokenAmount,
            tokenIdentifier: tokenIdentifier1,
            receiverSparkAddress: destinationAddress,
          },
          {
            tokenAmount,
            tokenIdentifier: tokenIdentifier2,
            receiverSparkAddress: destinationAddress,
          },
        ]),
      ).rejects.toThrow();
    });
  },
);
