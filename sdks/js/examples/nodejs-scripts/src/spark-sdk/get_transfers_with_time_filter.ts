import { SparkWallet } from "@buildonspark/spark-sdk";

async function main() {
  // Get mnemonic from command line arguments
  const mnemonic = process.argv[2] || "your_mnemonic_here";

  // Initialize wallet
  const { wallet, mnemonic: walletMnemonic } = await SparkWallet.initialize({
    mnemonicOrSeed: mnemonic,
    options: {
      network: "REGTEST",
    },
  });
  console.log("Wallet initialized with mnemonic:", walletMnemonic);

  console.log("Demonstrating time-filtered transfer queries...\n");

  // Example 1: Get transfers from the last 24 hours (exclusive)
  console.log("Example 1: Transfers from the last 24 hours");
  const oneDayAgo = new Date(Date.now() - 24 * 60 * 60 * 1000);
  const recentTransfers = await wallet.getTransfers(
    20, // limit
    0, // offset
    oneDayAgo, // createdAfter (exclusive - transfers strictly after this time)
    undefined, // createdBefore
  );
  console.log(
    `Found ${recentTransfers.transfers.length} transfers in the last 24 hours`,
  );
  for (const transfer of recentTransfers.transfers) {
    console.log(`  - Transfer ID: ${transfer.id}, Status: ${transfer.status}`);
  }
  console.log();

  // Example 2: Get transfers before a specific date (exclusive)
  // Note: createdAfter and createdBefore are mutually exclusive - only one can be used per query
  console.log("Example 2: Transfers before a specific date");
  const endDate = new Date("2025-12-12T23:59:59Z");
  const beforeTransfers = await wallet.getTransfers(
    20,
    0,
    undefined, // createdAfter not used in this example
    endDate, // Transfers strictly before 23:59:59 on Dec 12
  );
  console.log(
    `Found ${beforeTransfers.transfers.length} transfers before ${endDate.toISOString()}`,
  );
  console.log();

  // Example 3: Paginate through all transfers created after a date
  console.log("Example 3: Paginate through transfers after a cutoff date");
  const cutoffDate = new Date("2025-12-01T00:00:00Z");
  let offset = 0;
  let allTransfers: any[] = [];

  while (true) {
    const batch = await wallet.getTransfers(100, offset, cutoffDate);
    allTransfers = allTransfers.concat(batch.transfers);

    if (batch.offset === -1) {
      break; // No more results
    }
    offset = batch.offset;
  }

  console.log(
    `Total transfers strictly after ${cutoffDate.toISOString()}: ${allTransfers.length}`,
  );

  console.log("\nTime filtering demonstration complete!");
}

main().catch((error) => {
  console.error("Error:", error);
  process.exit(1);
});
