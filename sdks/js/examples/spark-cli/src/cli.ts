import readline from "readline";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import { getLatestDepositTxId, SparkWallet } from "@buildonspark/spark-sdk";
import { BitcoinNetwork } from "@buildonspark/spark-sdk/types";

// Initialize Spark Wallet
const walletMnemonic =
  "cctypical stereo dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  // Get network from command line args
  const network = process.argv.includes("mainnet") ? "MAINNET" : "REGTEST";
  let wallet: SparkWallet | undefined;

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
  });
  const helpMessage = `
  Available commands:
  initwallet [mnemonic | seed]                                        - Create a new wallet from a mnemonic or seed. If no mnemonic or seed is provided, a new mnemonic will be generated.
  getbalance                                                          - Get the wallet's balance
  getdepositaddress                                                   - Get an address to deposit funds from L1 to Spark
  getsparkaddress                                                     - Get the wallet's spark address
  getlatesttx <address>                                               - Get the latest deposit transaction id for an address
  claimdeposit <txid>                                                 - Claim any pending deposits to the wallet
  claimtransfers                                                      - Claim any pending transfers to the wallet
  createinvoice <amount> <memo>                                       - Create a new lightning invoice
  payinvoice <invoice>                                                - Pay a lightning invoice
  sendtransfer <amount> <receiverSparkAddress>                        - Send a spark transfer
  withdraw <amount> <onchainAddress>                                   - Withdraw funds to an L1 address
  coopfee <amount> <withdrawalAddress>                                - Get a fee estimate for a cooperative exit
  lightningsendfee <invoice>                                          - Get a fee estimate for a lightning send
  lightningreceivefee <amount> <REGTEST | MAINNET | TESTNET | SIGNET> - Get a fee estimate for a lightning receive
  getlightningsendrequest <requestId>                                 - Get a lightning send request by ID
  getlightningreceiverequest <requestId>                              - Get a lightning receive request by ID
  getcoopexitrequest <requestId>                                      - Get a coop exit request by ID
  sendtokentransfer <tokenPubKey> <amount> <receiverSparkAddress>     - Transfer tokens
  help                                                                - Show this help message
  exit/quit

  L1 commands:
  tokenwithdraw <tokenPublicKey> [tokenAmount] - Unilaterally withdraw tokens to L1- Exit the program
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
      case "getlatesttx":
        const latestTx = await getLatestDepositTxId(args[0]);
        console.log(latestTx);
        break;
      case "claimdeposit":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const depositResult = await wallet.claimDeposit(args[0]);
        console.log(depositResult);
        break;
      case "pendingtransfers":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const pendingTransfers = await wallet.getPendingTransfers();
        console.log(pendingTransfers);
        break;
      case "getlightningsendrequest":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const lightningSendRequest = await wallet.getLightningSendRequest(
          args[0],
        );
        console.log(lightningSendRequest);
        break;
      case "getlightningreceiverequest":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const lightningReceiveRequest = await wallet.getLightningReceiveRequest(
          args[0],
        );
        console.log(lightningReceiveRequest);
        break;
      case "getcoopexitrequest":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const coopExitRequest = await wallet.getCoopExitRequest(args[0]);
        console.log(coopExitRequest);
        break;
      case "claimtransfers":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const transferResult = await wallet.claimTransfers();
        console.log(transferResult);
        break;
      case "initwallet":
        const mnemonicOrSeed = args.join(" ");
        const options: ConfigOptions = {
          network: "REGTEST",
        };
        const { wallet: newWallet, mnemonic: newMnemonic } =
          await SparkWallet.initialize({
            mnemonicOrSeed,
            options,
          });
        wallet = newWallet;
        console.log("Mnemonic:", newMnemonic);
        break;
      case "getbalance":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const balanceInfo = await wallet.getBalance();
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
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const depositAddress = await wallet.getDepositAddress();
        console.log(depositAddress);
        break;
      case "getsparkaddress":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const sparkAddress = await wallet.getSparkAddress();
        console.log(sparkAddress);
        break;
      case "createinvoice":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const invoice = await wallet.createLightningInvoice({
          amountSats: parseInt(args[0]),
          memo: args[1],
        });
        console.log(invoice);
        break;
      case "payinvoice":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const payment = await wallet.payLightningInvoice({
          invoice: args[0],
        });
        console.log(payment);
        break;
      case "sendtransfer":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const transfer = await wallet.transfer({
          amountSats: parseInt(args[0]),
          receiverSparkAddress: args[1],
        });
        console.log(transfer);
        break;
      case "sendtokentransfer":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
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
          let errorMsg = "Unknown error";
          if (error instanceof Error) {
            errorMsg = error.message;
          }
          console.error(`Failed to transfer tokens: ${errorMsg}`);
        }
        break;
      case "withdraw":
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const withdrawal = await wallet.withdraw({
          amountSats: parseInt(args[0]),
          onchainAddress: args[1],
        });
        console.log(withdrawal);
        break;
      case "tokenwithdraw": {
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const tokenPublicKey = args[0];
        const amount = BigInt(parseInt(args[1]));

        let withdrawResult = await wallet.withdrawTokens(
          tokenPublicKey,
          amount,
        );
        if (withdrawResult) {
          console.log("Withdrawal L1 Transaction ID:", withdrawResult.txid);
        }
        break;
      }
      case "coopfee": {
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const fee = await wallet.getCoopExitFeeEstimate({
          amountSats: parseInt(args[0]),
          withdrawalAddress: args[1],
        });

        console.log(fee);
        break;
      }
      case "lightningsendfee": {
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const fee = await wallet.getLightningSendFeeEstimate({
          encodedInvoice: args[0],
        });
        console.log(fee);
        break;
      }
      case "lightningreceivefee": {
        if (!wallet) {
          console.log("Please initialize a wallet first");
          break;
        }
        const network = args[1] as BitcoinNetwork;
        const fee = await wallet.getLightningReceiveFeeEstimate({
          amountSats: parseInt(args[0]),
          network,
        });
        console.log(fee);
        break;
      }
    }
  }
}

runCLI();
