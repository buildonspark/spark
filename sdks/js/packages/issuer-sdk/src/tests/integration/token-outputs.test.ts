import { filterTokenBalanceForTokenIdentifier } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { SparkWalletTesting } from "@buildonspark/spark-sdk/test-utils";
import {
  getSingleIssuerTokenBalance,
  getSingleIssuerTokenIdentifier,
  mintSingleIssuerToken,
} from "../utils/multi-token-utils.js";
import { TEST_CONFIGS } from "./test-configs.js";

type TokenIdentifier = Parameters<
  typeof filterTokenBalanceForTokenIdentifier
>[1];
type TokenBalances = Parameters<typeof filterTokenBalanceForTokenIdentifier>[0];

const buildTransfers = ({
  count,
  tokenIdentifier,
  tokenAmount,
  receiverSparkAddress,
}: {
  count: number;
  tokenIdentifier: TokenIdentifier;
  tokenAmount: bigint;
  receiverSparkAddress: string;
}) =>
  Array.from({ length: count }, () => ({
    tokenIdentifier,
    tokenAmount,
    receiverSparkAddress,
  }));

const expectOutputCount = async ({
  wallet,
  tokenIdentifier,
  expectedCount,
}: {
  wallet: {
    getTokenOutputStats: (
      tokenIdentifier: TokenIdentifier,
    ) => Promise<{ outputCount: number }>;
  };
  tokenIdentifier: TokenIdentifier;
  expectedCount: number;
}) => {
  const outputStats = await wallet.getTokenOutputStats(tokenIdentifier);
  expect(outputStats).toBeDefined();
  expect(outputStats.outputCount).toBe(expectedCount);
};

const getOwnedBalance = async ({
  wallet,
  tokenIdentifier,
}: {
  wallet: { getBalance: () => Promise<unknown> };
  tokenIdentifier: TokenIdentifier;
}) => {
  const balanceObj = (await wallet.getBalance()) as
    | { tokenBalances?: TokenBalances }
    | null
    | undefined;
  return filterTokenBalanceForTokenIdentifier(
    balanceObj?.tokenBalances as TokenBalances,
    tokenIdentifier,
  ).ownedBalance;
};

describe.each(TEST_CONFIGS)(
  "token output tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should consolidate token outputs using optimizeTokenOutputs", async () => {
      const totalAmount = 10000n;
      const smallTransferAmount = 10n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      await issuerWallet.createToken({
        tokenName: `${name}OPT`,
        tokenTicker: "OPT",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      await mintSingleIssuerToken(issuerWallet, totalAmount);
      const tokenIdentifier =
        await getSingleIssuerTokenIdentifier(issuerWallet);
      expect(tokenIdentifier).toBeDefined();

      const userSparkAddress = await userWallet.getSparkAddress();
      const issuerSparkAddress = await issuerWallet.getSparkAddress();

      const transfersToUser = buildTransfers({
        count: 60,
        tokenIdentifier: tokenIdentifier!,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: userSparkAddress,
      });

      await issuerWallet.batchTransferTokens(transfersToUser);

      const transfersToIssuer = buildTransfers({
        count: 60,
        tokenIdentifier: tokenIdentifier!,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: issuerSparkAddress,
      });

      await userWallet.batchTransferTokens(transfersToIssuer);

      const balanceBeforeOptimization =
        await getSingleIssuerTokenBalance(issuerWallet);
      expect(balanceBeforeOptimization.balance).toBe(totalAmount);

      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenIdentifier!,
        expectedCount: 61,
      });

      await issuerWallet.optimizeTokenOutputs();

      await (issuerWallet as any).syncTokenOutputs();

      const balanceAfterOptimization =
        await getSingleIssuerTokenBalance(issuerWallet);
      expect(balanceAfterOptimization.balance).toBe(totalAmount);

      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenIdentifier!,
        expectedCount: 1,
      });

      await issuerWallet.transferTokens({
        tokenAmount: 100n,
        tokenIdentifier: tokenIdentifier!,
        receiverSparkAddress: userSparkAddress,
      });

      expect(
        await getOwnedBalance({
          wallet: userWallet,
          tokenIdentifier: tokenIdentifier!,
        }),
      ).toBe(100n);
    });

    it("should consolidate outputs for multiple token types using optimizeTokenOutputs", async () => {
      const totalAmountPerToken = 10000n;
      const smallTransferAmount = 10n;
      const transfersPerToken = 51;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { wallet: userWallet } = await SparkWalletTesting.initialize({
        options: config,
      });

      const tokenOne = await issuerWallet.createToken({
        tokenName: `${name}M1`,
        tokenTicker: "MT1",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
        returnIdentifierForCreate: true,
      });
      const tokenTwo = await issuerWallet.createToken({
        tokenName: `${name}M2`,
        tokenTicker: "MT2",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
        returnIdentifierForCreate: true,
      });

      const tokenOneIdentifier = tokenOne.tokenIdentifier;
      const tokenTwoIdentifier = tokenTwo.tokenIdentifier;

      await issuerWallet.mintTokens({
        tokenAmount: totalAmountPerToken,
        tokenIdentifier: tokenOneIdentifier,
      });
      await issuerWallet.mintTokens({
        tokenAmount: totalAmountPerToken,
        tokenIdentifier: tokenTwoIdentifier,
      });

      const userSparkAddress = await userWallet.getSparkAddress();
      const issuerSparkAddress = await issuerWallet.getSparkAddress();

      const toUserTokenOne = buildTransfers({
        count: transfersPerToken,
        tokenIdentifier: tokenOneIdentifier,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: userSparkAddress,
      });
      const toUserTokenTwo = buildTransfers({
        count: transfersPerToken,
        tokenIdentifier: tokenTwoIdentifier,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: userSparkAddress,
      });

      await issuerWallet.batchTransferTokens(toUserTokenOne);
      await issuerWallet.batchTransferTokens(toUserTokenTwo);

      const toIssuerTokenOne = buildTransfers({
        count: transfersPerToken,
        tokenIdentifier: tokenOneIdentifier,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: issuerSparkAddress,
      });
      const toIssuerTokenTwo = buildTransfers({
        count: transfersPerToken,
        tokenIdentifier: tokenTwoIdentifier,
        tokenAmount: smallTransferAmount,
        receiverSparkAddress: issuerSparkAddress,
      });

      await userWallet.batchTransferTokens(toIssuerTokenOne);
      await userWallet.batchTransferTokens(toIssuerTokenTwo);

      const balancesBeforeOptimization =
        await issuerWallet.getIssuerTokenBalances();
      expect(
        balancesBeforeOptimization.find(
          (balance) => balance.tokenIdentifier === tokenOneIdentifier,
        )?.balance,
      ).toBe(totalAmountPerToken);
      expect(
        balancesBeforeOptimization.find(
          (balance) => balance.tokenIdentifier === tokenTwoIdentifier,
        )?.balance,
      ).toBe(totalAmountPerToken);

      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenOneIdentifier,
        expectedCount: transfersPerToken + 1,
      });
      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenTwoIdentifier,
        expectedCount: transfersPerToken + 1,
      });

      await issuerWallet.optimizeTokenOutputs();

      const balancesAfterOptimization =
        await issuerWallet.getIssuerTokenBalances();
      expect(
        balancesAfterOptimization.find(
          (balance) => balance.tokenIdentifier === tokenOneIdentifier,
        )?.balance,
      ).toBe(totalAmountPerToken);
      expect(
        balancesAfterOptimization.find(
          (balance) => balance.tokenIdentifier === tokenTwoIdentifier,
        )?.balance,
      ).toBe(totalAmountPerToken);

      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenOneIdentifier,
        expectedCount: 1,
      });
      await expectOutputCount({
        wallet: issuerWallet,
        tokenIdentifier: tokenTwoIdentifier,
        expectedCount: 1,
      });
    });
  },
);
