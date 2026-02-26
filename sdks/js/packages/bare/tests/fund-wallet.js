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
  const depositAddress = await wallet.getSingleUseDepositAddress();
  if (!depositAddress) {
    throw new Error("Failed to get single-use deposit address");
  }

  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocks(3);

  await retryUntilSuccess(() => wallet.claimDeposit(signedTx.id));

  // Allow time for claim to process
  await new Promise((r) => setTimeout(r, 3000));

  const { balance } = await wallet.getBalance();
  return balance;
}

module.exports = { fundWallet };
