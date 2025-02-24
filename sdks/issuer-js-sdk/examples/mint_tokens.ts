import { IssuerWallet } from "../src/issuer-sdk";
import { Network } from "@buildonspark/spark-js-sdk/utils";

async function main() {
  try {
    // Replace with your test private key
    const walletMnemonic =
      "cctypical stereo dose party penalty decline neglect feel harvest abstract stage winter";

    // Create a wallet instance
    console.log("Creating wallet...");
    const wallet = new IssuerWallet(Network.REGTEST);
    await wallet.createWallet(walletMnemonic);

    // Amount to issue (e.g., 1M tokens)
    const amountToMint = BigInt("1000000");

    const result = await wallet.mintTokens(amountToMint);

    console.log("\nToken issuance successful!");
    console.log("Transaction details:");
    console.log(
      JSON.stringify(
        result,
        (_, value) =>
          typeof value === "bigint"
            ? value.toString()
            : value instanceof Uint8Array
            ? Buffer.from(value).toString("hex")
            : value,
        2
      )
    );
  } catch (error) {
    console.error("Error during token issuance:");
    console.error(error);
    process.exit(1);
  }
}

// Run the example
main();
