import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

async function main() {
    // Get mnemonic and receiver address from command line arguments
    const mnemonic = process.argv[2] || "your_mnemonic_here";
    const receiverAddress = process.argv[3] || "your_receiver_address_here";

    // Initialize wallet with configuration object
    const wallet = new SparkWallet(Network.REGTEST);

    const wallet_mnemonic = await wallet.initWallet(mnemonic);
    console.log("wallet mnemonic phrase:", wallet_mnemonic);

    const balance = await wallet.getBalance();
    console.log("Balance:", balance);

    const transfer = await wallet.sendSparkTransfer({
        receiverSparkAddress: receiverAddress,
        amountSats: 100
    });
    console.log("Transfer:", transfer);

    const new_balance = await wallet.getBalance();
    console.log("New Balance:", new_balance);
}

main().catch((error) => {
    console.error("Error:", error);
});
