import { createIssuerWallet } from "../src/client";
import { mintTokensOnSpark } from "../src/services/spark/mint";

async function main() {
  try {
    // Replace with your test private key
    const privateKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    
    // Create a wallet instance
    console.log("Creating wallet...");
    const wallet = createIssuerWallet(privateKey);
    
    // Replace with your token's public key
    const tokenPublicKey = "02eae4b876a8696134b868f88cc2f51f715f2dbedb7446b8e6edf3d4541c4eb7d9";
    
    // Amount to issue (e.g., 1M tokens)
    const amountToIssue = BigInt("1000000");
    
    const result = await mintTokensOnSpark(
      wallet.sparkWallet!,
      tokenPublicKey,
      amountToIssue
    );
    
    console.log("\nToken issuance successful!");
    console.log("Transaction details:");
    console.log(JSON.stringify(result, (_, value) =>
      typeof value === 'bigint'
        ? value.toString()
        : value instanceof Uint8Array
        ? Buffer.from(value).toString('hex')
        : value
    , 2));
    
  } catch (error) {
    console.error("Error during token issuance:");
    console.error(error);
    process.exit(1);
  }
}

// Run the example
main();
