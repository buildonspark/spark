// @ts-nocheck

import { Network } from "@buildonspark/spark-sdk/utils";
import readline from "readline";
import { IssuerWallet } from "../issuer-spark-wallet";

// Initialize Issuer Wallet
const walletMnemonic =
  "cctypical huge dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  let electrsCredentials = {
    username: "spark-sdk",
    password: "mCMk1JqlBNtetUNy",
  };

  let lrc20WalletApiConfig = {
    electrsCredentials,
  };

  let wallet = new IssuerWallet(Network.REGTEST);
  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  const helpMessage = `
  General commands:
  initwallet [mnemonic | seed]               - Create a new wallet from a mnemonic
  getaddresses                               - Get Spark and L1 addresses for this issuer
  help                                       - Show this help message
  exit/quit                                  - Exit the program

  L1 commands:
  announce <tokenName> <tokenTicker> <decimals> <maxSupply> <isFreezable> - Announce new token on L1
           <tokenName>   - string, from 3 to 20 symbols
           <tokenTicker> - string, from 3 to 6  symbols
           <decimals>    - uint8
           <maxSupply>   - uint128, set 0 if no restrictions are needed
           <isFreezable> - boolean, true/false
  withdraw [receiverPublicKey] - Unilaterally withdraw tokens to L1

  Spark commands:
  getbalance                                 - Show current wallet balance
  mint <amount>                              - Mint new tokens
  transfer <amount> <receiverPubKey>         - Transfer tokens to a recipient
  burn <amount>                              - Burn tokens
  freeze <publicKey>                         - Freeze tokens at the specified public key
  unfreeze <publicKey>                       - Unfreeze tokens at the specified public key
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

    try {
      switch (lowerCommand) {
        case "help":
          console.log(helpMessage);
          break;
        case "initwallet":
          const result = await wallet.initWallet(
            args.join(" "),
            true,
            lrc20WalletApiConfig,
          );
          console.log(result);
          break;
        case "getaddresses":
          if (!wallet.isSparkInitialized()) {
            console.log("No wallet initialized");
            break;
          }

          const tokenPublicKey = await wallet.getTokenPublicKey();
          console.log("Token Public Key:", tokenPublicKey);
          console.log("Spark Address:", tokenPublicKey);

          if (wallet.isL1Initialized()) {
            console.log("L1 Address:", wallet.getL1FundingAddress());

            const tokenPublicKeyInfo = await wallet.getIssuerTokenInfo();
            if (tokenPublicKeyInfo) {
              let announcement = tokenPublicKeyInfo.announcement;

              console.log("TokenInfo:");
              console.log("    Name:       ", announcement.name);
              console.log("    Ticker:     ", announcement.symbol);
              console.log("    Decimals:   ", announcement.decimal);
              console.log(
                "    MaxSupply:  ",
                announcement.maxSupply == 0
                  ? "unlimited"
                  : announcement.maxSupply,
              );
              console.log("    TotalSupply:", tokenPublicKeyInfo.totalSupply);
              console.log("    Freezable:  ", announcement.isFreezable);
            } else {
              console.log(
                "No TokenInfo found. You should announce the token on L1",
              );
            }
          }
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
            `Transferred ${transferAmount} tokens to ${receiverPubKey}`,
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
        case "getbalance":
          if (!wallet.isSparkInitialized()) {
            console.log("No wallet initialized");
            break;
          }
          const balanceInfo = await wallet.getBalance(true);
          // Display token balances if available
          console.log(`Token Balance: ${balanceInfo.balance}`);
          break;
        case "announce": {
          if (!wallet.isL1Initialized()) {
            console.log("No L1 wallet initialized");
            break;
          }

          const tokenName = args[0];
          const tokenTicker = args[1];
          const decimals = parseInt(args[2]);
          const maxSupply = BigInt(parseInt(args[3]));
          const isFreezable = args[4] === "true";

          if (tokenName.length < 3 || tokenName.length > 20) {
            console.log("Invalid tokenName length");
            break;
          }

          if (tokenTicker.length < 3 || tokenTicker.length > 6) {
            console.log("Invalid tokenTicker length");
            break;
          }

          if (decimals < 0) {
            console.log("Invalid decimals. Should be >= 0");
            break;
          }

          if (maxSupply < 0) {
            console.log("Invalid maxSupply. Should be >= 0");
            break;
          }

          let announcementResult = await wallet.announceTokenL1(
            tokenName,
            tokenTicker,
            decimals,
            maxSupply,
            isFreezable,
          );
          if (announcementResult) {
            console.log(
              "Token Announcement L1 Transaction ID:",
              announcementResult.txid,
            );
          }
          break;
        }
        case "withdraw": {
          if (!wallet.isL1Initialized()) {
            console.log("No L1 wallet initialized");
            break;
          }

          const receiverPublicKey = args[0];

          let withdrawResult = await wallet.withdrawTokens(receiverPublicKey);
          if (withdrawResult) {
            console.log("Withdrawal L1 Transaction ID:", withdrawResult.txid);
          }
          break;
        }
        default:
          console.log(`Unknown command: ${lowerCommand}`);
          console.log(helpMessage);
          break;
      }
    } catch (error) {
      console.error("Error executing command:", error.message);
      console.log("Please try again or type 'help' for available commands");
    }
  }
}

runCLI();
