/**
 * Shared wallet funding helper for bare integration tests.
 *
 * Uses single-use deposit addresses + direct SO claim (no SSP required).
 * Adapted from spark-sdk/src/tests/integration/deposit.test.ts.
 */

const { retryUntilSuccess } = require("./utils.js");
const { BitcoinFaucet } = require("./bare-faucet.js");

async function fundWallet(wallet, amount = 100000n) {
  const faucet = BitcoinFaucet.getInstance();

  const { balance: balanceBefore } = await wallet.getBalance();

  const depositAddress = await wallet.getSingleUseDepositAddress();
  if (!depositAddress) {
    throw new Error("Failed to get single-use deposit address");
  }

  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocks(3);

  await retryUntilSuccess(() => wallet.claimDeposit(signedTx.id));

  // Poll until the balance reflects the new deposit instead of using a fixed sleep.
  const { balance } = await retryUntilSuccess(async () => {
    const result = await wallet.getBalance();
    if (result.balance <= balanceBefore) {
      throw new Error(
        `Balance not yet updated (current: ${result.balance}, before: ${balanceBefore})`,
      );
    }
    return result;
  });

  return balance;
}

module.exports = { fundWallet };
