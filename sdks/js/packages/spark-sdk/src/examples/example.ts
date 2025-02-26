// @ts-nocheck

import { hexToBytes } from "@noble/curves/abstract/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
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
  getpubkey                                       - Get the wallet's identity public key
  gendepositaddr                                  - Generate a new deposit address, will poll to auto claim
  completedeposit <pubkey> <verifyingKey> <rawtx> - Complete a deposit
  createinvoice <amount> <memo>                   - Create a new lightning invoice
  payinvoice <invoice> <amount>                   - Pay a lightning invoice
  swap <targetAmount>                             - Swap leaves for a target amount
  balance                                         - Show current wallet balance
  getleaves                                       - Show current leaves
  sendtransfer <amount> <receiverPubKey>          - Send a transfer
  pendingtransfers                                - Show pending transfers
  claimtransfer <transferId>                      - Claim a pending transfer
  claimall                                        - Claim all pending transfers
  coopexit <onchainAddress> <targetAmount>        - Coop exit
  clear                                           - Cancel all sender initiated transfers
  tokentransfer <tokenPubKey> <amount> <receiverPubKey> - Transfer tokens
  tokenbalance [tokenPubKey]                      - Show token balance for specific token or all tokens
  tokenconsolidate <tokenPubKey>                 - Consolidate token leaves for a specific token
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
      case "getpubkey":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        console.log(await wallet.getIdentityPublicKey());
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
        if (!depositAddress.depositAddress) {
          console.log("No deposit address");
          break;
        }

        while (true) {
          const nodes = await wallet.claimDeposits();
          if (nodes && nodes.length > 0) {
            console.log("Claimed deposits", nodes);
            break;
          }
          console.log("Waiting for deposits to be claimed");
          await new Promise((resolve) => setTimeout(resolve, 5000));
        }

        break;
      case "clear":
        await wallet.cancelAllSenderInitiatedTransfers();
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
      case "tokentransfer":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        if (args.length < 3) {
          console.log(
            "Usage: tokentransfer <tokenPubKey> <amount> <receiverPubKey>"
          );
          break;
        }

        const tokenPubKey = args[0];
        const tokenAmount = BigInt(parseInt(args[1]));
        const tokenReceiverPubKey = args[2];

        try {
          await wallet.transferTokens(
            tokenPubKey,
            tokenAmount,
            tokenReceiverPubKey
          );
          console.log(
            `Successfully transferred ${tokenAmount} tokens to ${tokenReceiverPubKey}`
          );
        } catch (error) {
          console.error("Failed to transfer tokens:", error.message);
        }
        break;
      case "tokenbalance":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        try {
          const allTokenBalances = await wallet.getAllTokenBalances();

          if (args.length > 0) {
            // If a specific token public key is provided, show only that token's balance
            const tokenPubKey = args[0];
            if (allTokenBalances.has(tokenPubKey)) {
              console.log(
                `Token balance for ${tokenPubKey}: ${allTokenBalances.get(
                  tokenPubKey
                )}`
              );
            } else {
              console.log(`No balance found for token ${tokenPubKey}`);
            }
          } else {
            // If no token public key is provided, show all token balances
            if (allTokenBalances.size === 0) {
              console.log("No token balances found");
            } else {
              console.log("All token balances:");
              for (const [tokenPubKey, balance] of allTokenBalances.entries()) {
                console.log(
                  `  ${tokenPubKey}: ${balance.totalAmount} tokens (${balance.numLeaves} leaves)`
                );
              }
            }
          }
        } catch (error) {
          console.error("Failed to get token balances:", error.message);
        }
        break;
      case "tokenconsolidate":
        if (!wallet.isInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        if (args.length < 1) {
          console.log("Usage: tokenconsolidate <tokenPubKey>");
          break;
        }

        const tokenPubKeyToConsolidate = args[0];

        try {
          await wallet.consolidateTokenLeaves(tokenPubKeyToConsolidate);
          console.log(
            `Successfully consolidated token leaves for ${tokenPubKeyToConsolidate}`
          );
        } catch (error) {
          console.error("Failed to consolidate token leaves:", error.message);
        }
        break;
    }
  }
}

runCLI();
