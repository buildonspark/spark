import readline from "readline";
import { SparkWallet } from "../../dist/spark-sdk";

// Initialize Spark Wallet
const walletMnemonic =
  "cctypical stereo dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  // Get network from command line args
  const network = process.argv.includes("mainnet") ? "MAINNET" : "REGTEST";
  let wallet = new SparkWallet(network);

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  const helpMessage = `
  Available commands:
  initwallet [mnemonic | seed]                                    - Create a new wallet from a mnemonic or seed. If no mnemonic or seed is provided, a new mnemonic will be generated.
  getbalance                                                      - Get the wallet's balance
  getdepositaddress                                               - Get an address to deposit funds from L1 to Spark
  getsparkaddress                                                 - Get the wallet's spark address
  claimdeposit <txid>                                             - Claim any pending deposits to the wallet
  createinvoice <amount> <memo>                                   - Create a new lightning invoice
  payinvoice <invoice>                                            - Pay a lightning invoice
  sendtransfer <amount> <receiverSparkAddress>                    - Send a spark transfer
  withdraw <onchainAddress> <amount>                              - Withdraw funds to an L1 address
  sendtokentransfer <tokenPubKey> <amount> <receiverSparkAddress> - Transfer tokens
  help                                                            - Show this help message
  exit/quit

  L1 commands:
  tokenwithdraw <tokenPublicKey> [receiverPublicKey] - Unilaterally withdraw tokens to L1- Exit the program
`;
  console.log(helpMessage);

  while (true) {
    const command = await new Promise<string>((resolve) => {
      rl.question("> ", resolve);
    });

    const [firstWord, ...args] = command.split(" ");
    const lowerCommand = firstWord.toLowerCase();

    if (lowerCommand === "exit" || lowerCommand === "quit") {
      rl.close();
      break;
    }

    switch (lowerCommand) {
      case "help":
        console.log(helpMessage);
        break;
      case "claimdeposit":
        const depositResult = await wallet.claimDeposit(args[0]);
        console.log(depositResult);
        break;
      case "initwallet":
        wallet = new SparkWallet(network);
        const result = await wallet.initWallet(args.join(" "));
        console.log(result);
        break;
      case "getbalance":
        const balanceInfo = await wallet.getBalance(true);
        console.log("Sats Balance: " + balanceInfo.balance);
        if (balanceInfo.tokenBalances && balanceInfo.tokenBalances.size > 0) {
          console.log("\nToken Balances:");
          for (const [
            tokenPublicKey,
            tokenInfo,
          ] of balanceInfo.tokenBalances.entries()) {
            console.log(`  Token (${tokenPublicKey}):`);
            console.log(`    Balance: ${tokenInfo.balance}`);
          }
        }
        break;
      case "getdepositaddress":
        const depositAddress = await wallet.getDepositAddress();
        console.log(depositAddress);
        break;
      case "getsparkaddress":
        const sparkAddress = await wallet.getSparkAddress();
        console.log(sparkAddress);
        break;
      case "createinvoice":
        const invoice = await wallet.createLightningInvoice({
          amountSats: parseInt(args[0]),
          memo: args[1],
        });
        console.log(invoice);
        break;
      case "payinvoice":
        const payment = await wallet.payLightningInvoice({
          invoice: args[0],
        });
        console.log(payment);
        break;
      case "sendtransfer":
        const transfer = await wallet.sendSparkTransfer({
          amountSats: parseInt(args[0]),
          receiverSparkAddress: args[1],
        });
        console.log(transfer);
        break;
      case "sendtokentransfer":
        if (args.length < 3) {
          console.log(
            "Usage: sendtokentransfer <tokenPubKey> <amount> <receiverPubKey>",
          );
          break;
        }

        const tokenPubKey = args[0];
        const tokenAmount = BigInt(parseInt(args[1]));
        const tokenReceiverPubKey = args[2];

        try {
          const result = await wallet.transferTokens({
            tokenPublicKey: tokenPubKey,
            tokenAmount: tokenAmount,
            receiverSparkAddress: tokenReceiverPubKey,
          });
          console.log(result);
        } catch (error) {
          console.error("Failed to transfer tokens:", error.message);
        }
        break;
      case "withdraw":
        const withdrawal = await wallet.withdraw({
          onchainAddress: args[0],
          targetAmountSats: parseInt(args[1]),
        });
        console.log(withdrawal);
        break;
      case "tokenwithdraw": {
        const tokenPublicKey = args[0];
        const receiverPublicKey = args[1];

        let withdrawResult = await wallet.withdrawTokens(
          tokenPublicKey,
          receiverPublicKey,
        );
        if (withdrawResult) {
          console.log("Withdrawal L1 Transaction ID:", withdrawResult.txid);
        }
        break;
      }
    }
  }
}

runCLI();
