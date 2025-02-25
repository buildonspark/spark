import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Transaction } from "@scure/btc-signer";
import { Buffer } from "buffer";
import readline from "readline";
import { SparkWallet } from "../../dist/spark-sdk";
import { getTxFromRawTxHex } from "../../dist/utils/bitcoin";
import { Network } from "../../dist/utils/network";

// Initialize Spark Wallet
const walletMnemonic =
  "cctypical stereo dose party penalty decline neglect feel harvest abstract stage winter";

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
  payinvoice <invoice> <amount>
  swap <targetAmount>                             - Swap leaves for a target amount
  balance                                         - Show current wallet balance
  getleaves                                       - Show current leaves
  sendtransfer <amount> <receiverPubKey>          - Send a transfer
  pendingtransfers                                - Show pending transfers
  claimtransfer <transferId>                      - Claim a pending transfer
  coopexit <onchainAddress> <targetAmount>        - Coop exit
  claim                                           - Claim all pending transfers
  help                                            - Show this help message
  exit/quit                                       - Exit the program
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
      case "genmnemonic":
        const mnemonic = generateMnemonic(wordlist);
        console.log(mnemonic);
        break;
      case "initwallet":
        const walletMnemonic = await wallet.initWalletFromMnemonic(
          args.length > 0 ? args.join(" ") : undefined
        );
        console.log("mnemonic", walletMnemonic);
        break;
      case "gendepositaddr":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const leafPubKey = hexToBytes(await wallet.generatePublicKey());
        const depositAddress = await wallet.generateDepositAddress(leafPubKey);
        console.log("Deposit address:", depositAddress.depositAddress?.address);
        console.log(
          "Verifying key:",
          typeof depositAddress.depositAddress?.verifyingKey === "object"
            ? Buffer.from(depositAddress.depositAddress.verifyingKey).toString(
                "hex"
              )
            : depositAddress.depositAddress?.verifyingKey
        );
        console.log("Pubkey:", bytesToHex(leafPubKey));
        if (!depositAddress.depositAddress) {
          console.log("No deposit address");
          break;
        }

        while (true) {
          let depositTx: Transaction | null = null;
          let vout = 0;
          try {
            const baseUrl =
              "https://regtest-mempool.dev.dev.sparkinfra.net/api";
            const auth = btoa("lightspark:TFNR6ZeLdxF9HejW");

            const response = await fetch(
              `${baseUrl}/address/${depositAddress}/txs`,
              {
                headers: {
                  Authorization: `Basic ${auth}`,
                  "Content-Type": "application/json",
                },
              }
            );

            const addressTxs = await response.json();

            if (addressTxs && addressTxs.length > 0) {
              const latestTx = addressTxs[0];

              // // Find our output
              const outputIndex = latestTx.vout.findIndex(
                (output: any) => output.scriptpubkey_address === depositAddress
              );

              if (outputIndex === -1) {
                return null;
              }

              const txResponse = await fetch(
                `${baseUrl}/tx/${latestTx.txid}/hex`,
                {
                  headers: {
                    Authorization: `Basic ${auth}`,
                    "Content-Type": "application/json",
                  },
                }
              );
              const txHex = await txResponse.text();
              depositTx = getTxFromRawTxHex(txHex);
              vout = outputIndex;
              break;
            }
          } catch (error) {
            throw error;
          }

          if (depositTx) {
            const nodes = await wallet.finalizeDeposit(
              leafPubKey,
              depositAddress.depositAddress?.verifyingKey,
              depositTx,
              vout
            );
            console.log("Created new leaf node", nodes);
            break;
          }
          console.log("Waiting for deposit tx");
          await new Promise((resolve) => setTimeout(resolve, 5000));
        }

        break;
      case "completedeposit":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const depositTx = getTxFromRawTxHex(args[2]);

        const treeResp = await wallet.finalizeDeposit(
          hexToBytes(args[0]),
          hexToBytes(args[1]),
          depositTx,
          0
        );
        console.log("Tree root:", treeResp);
        break;
      case "createinvoice":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const invoice = await wallet.createLightningInvoice({
          amountSats: parseInt(args[0]),
          memo: args[1],
          expirySeconds: 60 * 60 * 24,
        });

        console.log("Invoice created:", invoice);
        break;
      case "sendtransfer":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const receiverPubKey = hexToBytes(args[1]);
        const amount = parseInt(args[0]);
        await wallet.sendTransfer({
          amount,
          receiverPubKey,
        });
        break;
      case "coopexit":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const coopExitResult = await wallet.coopExit(
          args[0],
          parseInt(args[1])
        );
        console.log(coopExitResult);
        break;
      case "claimall":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        console.log(await wallet.getBalance());
        break;
      case "payinvoice":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const payResult = await wallet.payLightningInvoice({
          invoice: args[0],
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
    }
  }
}

runCLI();
