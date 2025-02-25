// @ts-nocheck

import { hexToBytes } from "@noble/curves/abstract/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import readline from "readline";
import { IssuerWallet } from "../../dist/issuer-sdk";
import { Network } from "@buildonspark/spark-js-sdk/utils";

// Initialize Issuer Wallet
const walletMnemonic =
  "cctypical huge dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  let wallet = new IssuerWallet(Network.REGTEST);
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  const helpMessage = `
  Available commands:
  genmnemonic                                - Generate a new mnemonic
  initwallet <mnemonic>                      - Create a new wallet from a mnemonic
  tokenpublickey                             - Get the token public key
  mint <amount>                              - Mint new tokens
  transfer <amount> <receiverPubKey>         - Transfer tokens to a recipient
  burn <amount>                              - Burn tokens
  freeze <publicKey>                         - Freeze tokens at the specified public key
  unfreeze <publicKey>                       - Unfreeze tokens at the specified public key
  consolidate                                - Consolidate token leaves
  balance                                    - Show current wallet balance
  help                                       - Show this help message
  exit/quit                                  - Exit the program
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
        await wallet.initWalletFromMnemonic(
          args.length > 0 ? args.join(" ") : walletMnemonic
        );
        console.log("Wallet initialized successfully");
        break;
      case "tokenpublickey":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const tokenPublicKey = await wallet.getTokenPublicKey();
        console.log("Token Public Key:", tokenPublicKey);
        break;
      case "mint":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const amountToMint = BigInt(parseInt(args[0]));
        await wallet.mintTokens(amountToMint);
        console.log(`Minted ${amountToMint} tokens`);
        break;
      case "transfer":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const transferAmount = BigInt(parseInt(args[0]));
        const receiverPubKey = args[1];
        await wallet.transferTokens(transferAmount, receiverPubKey);
        console.log(
          `Transferred ${transferAmount} tokens to ${receiverPubKey}`
        );
        break;
      case "burn":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const amountToBurn = BigInt(parseInt(args[0]));
        await wallet.burnTokens(amountToBurn);
        console.log(`Burned ${amountToBurn} tokens`);
        break;
      case "freeze":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const freezePublicKey = args[0];
        const freezeResult = await wallet.freezeTokens(freezePublicKey);
        console.log("Freeze result:", freezeResult);
        break;
      case "unfreeze":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        const unfreezePublicKey = args[0];
        const unfreezeResult = await wallet.unfreezeTokens(unfreezePublicKey);
        console.log("Unfreeze result:", unfreezeResult);
        break;
      case "consolidate":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }

        await wallet.consolidateTokens();
        console.log("Token leaves consolidated");
        break;
      case "balance":
        if (!wallet.isSparkInitialized()) {
          console.log("No wallet initialized");
          break;
        }
        const balanceInfo = await wallet.getTokenBalance();
        console.log("Balance:", balanceInfo.balance);
        console.log("Number of token leaves:", balanceInfo.leafCount);
        break;
      default:
        console.log(`Unknown command: ${lowerCommand}`);
        console.log(helpMessage);
        break;
    }
  }
}

runCLI();
