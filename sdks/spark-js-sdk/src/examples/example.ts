import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import readline from "readline";
import { BitcoinNetwork } from "../../dist/graphql/objects";
import { SparkWallet } from "../../dist/spark-sdk";
import { getTxFromRawTxHex } from "../../dist/utils/bitcoin";
import { Network } from "../../dist/utils/network";

// Initialize Spark Wallet
const walletMnemonic =
  "typical stereo dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  let wallet = new SparkWallet(Network.REGTEST);
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  const helpMessage = `
  Available commands:
  genmnemonic                                     - Generate a new mnemonic
  initwallet <mnemonic>                           - Create a new wallet from a mnemonic
  gendepositaddr                                  - Generate a new deposit address
  completedeposit <pubkey> <verifyingKey> <rawtx> - Complete a deposit
  createinvoice <amount> <memo>                   - Create a new lightning invoice
  payinvoice <invoice> <amount>                   - Pay a lightning invoice
  balance                                         - Show current wallet balance
  getleaves                                       - Show current leaves
  pending                                         - Show pending transfers
  claimtransfer <transferId>                      - Claim a pending transfer
  help                                            - Show this help message
  exit/quit                                       - Exit the program
`;
  console.log(helpMessage);

  while (true) {
    const command = await new Promise<string>((resolve) => {
      rl.question("> ", resolve);
    });

    const [firstWord, ...rest] = command.split(" ");
    const args = rest.join(" ");
    const lowerCommand = firstWord.toLowerCase();

    if (lowerCommand === "exit" || lowerCommand === "quit") {
      rl.close();
      break;
    }

    switch (lowerCommand) {
      case "help":
        console.log(helpMessage);
        break;
      case "genmnemonic":
        const mnemonic = wallet.generateMnemonic();
        console.log(mnemonic);
        break;
      case "initwallet":
        console.log(`:${args}:`);
        const pubKey = await wallet.createSparkWallet(args || walletMnemonic);
        console.log("pubkey", pubKey);
        break;
      case "gendepositaddr":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const leafPubKey = wallet.getSigner().generatePublicKey();
        const depositAddress = await wallet.generateDepositAddress(leafPubKey);
        console.log("Deposit address:", depositAddress.depositAddress?.address);
        console.log(
          "Verifying key:",
          bytesToHex(
            depositAddress.depositAddress?.verifyingKey || new Uint8Array()
          )
        );
        console.log("Pubkey:", bytesToHex(leafPubKey));
        break;
      case "completedeposit":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const depositTx = getTxFromRawTxHex(args[2]);

        const treeResp = await wallet.createTreeRoot(
          hexToBytes(args[0]),
          hexToBytes(args[1]),
          depositTx,
          0
        );
        console.log("Tree root:", treeResp.nodes);
        break;
      case "createinvoice":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const invoice = await wallet.createLightningInvoice({
          amountSats: parseInt(args),
          memo: args[1],
          expirySeconds: 60 * 60 * 24,
        });

        const fee = await wallet.getLightningReceiveFeeEstimate({
          amountSats: parseInt(args),
          network: BitcoinNetwork.REGTEST,
        });
        console.log("Invoice created:", invoice);
        console.log(
          `Fee: ${fee?.feeEstimate.originalValue} ${fee?.feeEstimate.originalUnit}`
        );
        break;
      case "pending":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const pending = await wallet.queryPendingTransfers();
        console.log(pending);
        break;
      case "claimtransfer":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        if (!args) {
          console.log("Please provide a transfer id");
          break;
        }
        const pendingTransfers = await wallet.queryPendingTransfers();
        const transfer = pendingTransfers.transfers.find((t) => t.id === args);
        if (!transfer) {
          console.log("Transfer not found");
          break;
        }
        const result = await wallet.claimTransfer(transfer);
        console.log(result.nodes);
        break;
      case "payinvoice":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const payResult = await wallet.payLightningInvoice({
          invoice: args[0],
          idempotencyKey: args[0],
          amountSats: parseInt(args[1]),
        });
        console.log(payResult);
        break;
      case "balance":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const balance = await wallet.getBalance();
        console.log(balance);
        break;
      case "getleaves":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const leaves = await wallet.getLeaves();
        console.log(leaves);
        break;
    }
  }
}

await runCLI();
