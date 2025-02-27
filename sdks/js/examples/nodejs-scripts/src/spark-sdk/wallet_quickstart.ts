import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

async function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function main() {
  try {
    console.log("Starting complete Spark workflow demo...");
    console.log("----------------------------------------");

    // Step 1: Create two wallets
    console.log("Step 1: Creating two Spark wallets");
    const wallet1 = new SparkWallet(Network.REGTEST);
    const wallet2 = new SparkWallet(Network.REGTEST);

    // Initialize both wallets and store their mnemonics
    const wallet1Mnemonic = await wallet1.initWallet();
    const wallet2Mnemonic = await wallet2.initWallet();

    console.log("Wallet 1 mnemonic:", wallet1Mnemonic);
    console.log("Wallet 2 mnemonic:", wallet2Mnemonic);
    console.log("----------------------------------------");

    // Step 2: Generate deposit addresses for funding from L1
    console.log("Step 2: Generating deposit addresses for L1 funding");
    const wallet1DepositAddress = await wallet1.getDepositAddress();
    const wallet2DepositAddress = await wallet2.getDepositAddress();

    console.log("Wallet 1 deposit address (for funding from L1):", wallet1DepositAddress);
    console.log("Wallet 2 deposit address (for funding from L1):", wallet2DepositAddress);
    console.log("----------------------------------------");

    // Step 3: Get Spark addresses for both wallets
    console.log("Step 3: Getting Spark addresses for both wallets");
    const wallet1SparkAddress = await wallet1.getSparkAddress();
    const wallet2SparkAddress = await wallet2.getSparkAddress();

    console.log("Wallet 1 Spark address:", wallet1SparkAddress);
    console.log("Wallet 2 Spark address:", wallet2SparkAddress);
    console.log("----------------------------------------");

    // Step 4: Wait for funding from L1 to Wallet 1
    console.log("Step 4: Waiting for funding from L1 to Wallet 1");
    console.log(`Please send Bitcoin to this address: ${wallet1DepositAddress}`);

    console.log("Checking for balance every 30 seconds...");
    
    let wallet1Balance = { balance: BigInt(0), tokenBalances: new Map<string, { balance: bigint }>() };
    let fundingReceived = false;
    
    // Poll for balance updates until funding is received
    while (!fundingReceived) {
      wallet1Balance = await wallet1.getBalance();
      console.log(`Current Wallet 1 balance: ${wallet1Balance.balance.toString()} sats`);
      
      if (wallet1Balance.balance > BigInt(0)) {
        fundingReceived = true;
        console.log("Funding received successfully!");
      } else {
        console.log("Waiting for funding... (will check again in 10 seconds)");
        await sleep(10000); // Wait 30 seconds before checking again
      }
    }
    console.log("----------------------------------------");

    // Step 5: Send payment from Wallet 1 to Wallet 2
    console.log("Step 5: Sending payment from Wallet 1 to Wallet 2");
    
    // Calculate amount to send (75% of available balance)
    const transferAmount = Number((wallet1Balance.balance * BigInt(75)) / BigInt(100));
    console.log(`Sending ${transferAmount} sats from Wallet 1 to Wallet 2`);
    
    const transfer = await wallet1.sendSparkTransfer({
      receiverSparkAddress: wallet2SparkAddress,
      amountSats: transferAmount
    });
    
    console.log("Transfer completed successfully!");
    console.log("Transfer details:", transfer);
    console.log("----------------------------------------");
    
    // Step 6: Display balances of both wallets
    console.log("Step 6: Displaying balances of both wallets after transfer");
    
    const wallet1BalanceAfterTransfer = await wallet1.getBalance();
    const wallet2BalanceAfterTransfer = await wallet2.getBalance();
    
    console.log(`Wallet 1 balance: ${wallet1BalanceAfterTransfer.balance.toString()} sats`);
    console.log(`Wallet 2 balance: ${wallet2BalanceAfterTransfer.balance.toString()} sats`);
    console.log("----------------------------------------");
    
    // Step 7: Withdraw funds from Wallet 2 back to L1
    console.log("Step 7: Withdrawing funds from Wallet 2 back to L1");
    
    // You'll need to specify an L1 address to withdraw to
    const l1WithdrawalAddress = "bcrt1qztztqzh4c935q9lnupq36f07ea7l9r08ex6nfz9jqqefw3lcq9f0qsqjjy";
    console.log(`Withdrawing funds to L1 address: ${l1WithdrawalAddress}`);
    
    // Withdraw all available funds (no targetAmountSats specified)
    const withdrawal = await wallet2.withdraw({
      onchainAddress: l1WithdrawalAddress
    });
    
    if (withdrawal) {
      console.log("Withdrawal initiated successfully!");
      console.log("Withdrawal details:", withdrawal);
    } else {
      console.log("Withdrawal could not be completed. This could be due to insufficient funds or network issues.");
    }
    console.log("----------------------------------------");
    
    // Step 8: Final balances
    console.log("Step 8: Final balances after withdrawal");
    
    const wallet1FinalBalance = await wallet1.getBalance();
    const wallet2FinalBalance = await wallet2.getBalance();
    
    console.log(`Wallet 1 final balance: ${wallet1FinalBalance.balance.toString()} sats`);
    console.log(`Wallet 2 final balance: ${wallet2FinalBalance.balance.toString()} sats`);
    console.log("----------------------------------------");
    
    console.log("Complete workflow demonstration finished!");
    console.log("");
    console.log("Note: In a real environment, L1 transactions might take time to confirm.");
    console.log("The withdrawal process may take several minutes to complete.");
    console.log("For testing purposes, you can use REGTEST or TESTNET to avoid using real Bitcoin.");
    
  } catch (error) {
    console.error("Error in workflow:", error);
  }
}

// Execute the main function
main().catch((error) => {
  console.error("Fatal error:", error);
});