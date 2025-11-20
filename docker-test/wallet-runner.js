const { SparkWallet } = require("@buildonspark/spark-sdk");

async function main() {
  const mnemonic = process.env.SPARK_WALLET_MNEMONIC || "MNEMONIC_HERE";

  console.log(
    "Starting SparkWallet with mnemonic (first 4 words):",
    mnemonic.split(" ").slice(0, 4).join(" ")
  );

  const { wallet } = await SparkWallet.initialize({ mnemonicOrSeed: mnemonic });

  console.log("Wallet initialized, starting periodic balance checks…");

  setInterval(async () => {
    try {
      let balance = await wallet.getBalance();
      console.log(new Date().toISOString(), "balance", balance);
    } catch (err) {
      console.error("getBalance failed:", err);
    }
  }, 2_000);
}

main().catch((err) => {
  console.error("Fatal error starting wallet:", err);
  process.exit(1);
});
