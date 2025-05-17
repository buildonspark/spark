import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { getLatestDepositTxId } from "@buildonspark/spark-sdk";
import { TokenTransactionStatus } from "@buildonspark/spark-sdk/proto/spark";
import {
  ConfigOptions,
  LOCAL_WALLET_CONFIG,
  MAINNET_WALLET_CONFIG,
  REGTEST_WALLET_CONFIG,
} from "@buildonspark/spark-sdk/services/wallet-config";
import { ExitSpeed } from "@buildonspark/spark-sdk/types";
import {
  getNetwork,
  getP2TRScriptFromPublicKey,
  Network,
  NetworkType,
} from "@buildonspark/spark-sdk/utils";
import { hexToBytes } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { hex } from "@scure/base";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import readline from "readline";
import fs from "fs";
import { decodeSparkAddress } from "@buildonspark/spark-sdk/address";

const commands = [
  "initwallet",
  "getbalance",
  "getdepositaddress",
  "getsparkaddress",
  "getlatesttx",
  "claimdeposit",
  "createinvoice",
  "payinvoice",
  "sendtransfer",
  "withdraw",
  "withdrawalfee",
  "lightningsendfee",
  "getlightningsendrequest",
  "getlightningreceiverequest",
  "getcoopexitrequest",
  "gettransfers",
  "transfertokens",
  "gettokenl1address",
  "getissuertokenbalance",
  "getissuertokeninfo",
  "getissuertokenpublickey",
  "minttokens",
  "burntokens",
  "freezetokens",
  "unfreezetokens",
  "getissuertokenactivity",
  "announcetoken",
  "nontrustydeposit",
  "querytokentransactions",
  "help",
  "exit",
  "quit",
];

// Initialize Spark Wallet
const walletMnemonic =
  "cctypical stereo dose party penalty decline neglect feel harvest abstract stage winter";

async function runCLI() {
  // Get network from environment variable
  const network = (() => {
    const envNetwork = process.env.NETWORK?.toUpperCase();
    if (envNetwork === "MAINNET") return "MAINNET";
    if (envNetwork === "LOCAL") return "LOCAL";
    return "REGTEST"; // default
  })();

  const configFile = process.env.CONFIG_FILE;
  let config: ConfigOptions = {};
  if (configFile) {
    try {
      const data = fs.readFileSync(configFile, "utf8");
      config = JSON.parse(data);
      if (config.network !== network) {
        console.error("Network mismatch in config file");
        return;
      }
    } catch (err) {
      console.error("Error reading config file:", err);
      return;
    }
  } else {
    switch (network) {
      case "MAINNET":
        config = MAINNET_WALLET_CONFIG;
        break;
      case "REGTEST":
        config = REGTEST_WALLET_CONFIG;
        break;
      default:
        config = LOCAL_WALLET_CONFIG;
        break;
    }
  }

  let wallet: IssuerSparkWallet | undefined;

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
    completer: (line: string) => {
      const completions = commands.filter((c) => c.startsWith(line));
      return [completions.length ? completions : commands, line];
    },
  });
  const helpMessage = `
  Available commands:
  initwallet [mnemonic | seed]                                        - Create a new wallet from a mnemonic or seed. If no mnemonic or seed is provided, a new mnemonic will be generated.
  getbalance                                                          - Get the wallet's balance
  getdepositaddress                                                   - Get an address to deposit funds from L1 to Spark
  identity                                                            - Get the wallet's identity public key
  getsparkaddress                                                     - Get the wallet's spark address
  decodesparkaddress <sparkAddress> <network(MAINNET|REGTEST|SIGNET|TESTNET|LOCAL))> - Decode a spark address to get the identity public key
  getlatesttx <address>                                               - Get the latest deposit transaction id for an address
  claimdeposit <txid>                                                 - Claim any pending deposits to the wallet
  gettransfers [limit] [offset]                                       - Get a list of transfers
  createinvoice <amount> <memo>                                       - Create a new lightning invoice
  payinvoice <invoice> <maxFeeSats>                                   - Pay a lightning invoice
  sendtransfer <amount> <receiverSparkAddress>                        - Send a spark transfer
  withdraw <amount> <onchainAddress> <exitSpeed(FAST|MEDIUM|SLOW)>    - Withdraw funds to an L1 address
  withdrawalfee <amount> <withdrawalAddress>                          - Get a fee estimate for a withdrawal (cooperative exit)
  lightningsendfee <invoice>                                          - Get a fee estimate for a lightning send
  getlightningsendrequest <requestId>                                 - Get a lightning send request by ID
  getlightningreceiverequest <requestId>                              - Get a lightning receive request by ID
  getcoopexitrequest <requestId>                                      - Get a coop exit request by ID

  Token Holder Commands:
  transfertokens <tokenPubKey> <amount> <receiverSparkAddress>        - Transfer tokens

  Token Issuer Commands:
  gettokenl1address                                                   - Get the L1 address for on-chain token operations
  getissuertokenbalance                                               - Get the issuer's token balance
  getissuertokeninfo                                                  - Get the issuer's token information
  getissuertokenpublickey                                             - Get the issuer's token public key
  minttokens <amount>                                                 - Mint new tokens
  burntokens <amount>                                                 - Burn tokens
  freezetokens <sparkAddress>                                         - Freeze tokens for a specific address
  unfreezetokens <sparkAddress>                                       - Unfreeze tokens for a specific address
  getissuertokenactivity                                              - Get issuer's token activity
  announcetoken <tokenName> <tokenTicker> <decimals> <maxSupply> <isFreezable> - Announce token on L1

  help                                                                - Show this help message
  exit/quit                                                           - Exit the program
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
      wallet?.cleanupConnections();
      break;
    }

    try {
      switch (lowerCommand) {
        case "help":
          console.log(helpMessage);
          break;
        case "nontrustydeposit":
          if (process.env.NODE_ENV !== "development" || network !== "REGTEST") {
            console.log(
              "This command is only available in the development environment and on the REGTEST network",
            );
            break;
          }
          /**
           * This is an example of how to create a non-trusty deposit. Real implementation may differ.
           *
           * 1. Get an address to deposit funds from L1 to Spark
           * 2. Construct a tx spending from the L1 address to the Spark address
           * 3. Call initalizeDeposit with the tx hex
           * 4. Sign the tx
           * 5. Broadcast the tx
           */

          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          if (args.length !== 1) {
            console.log("Usage: nontrustydeposit <destinationBtcAddress>");
            break;
          }

          const privateKey =
            "9303c68c414a6208dbc0329181dd640b135e669647ad7dcb2f09870c54b26ed9";

          // IMPORTANT: This address needs to be funded with regtest BTC before running this example
          const sourceAddress =
            "bcrt1pzrfhq4gm7kuww875lkj27cx005x08g2jp6qxexnu68gytn7sjqss3s6j2c";

          try {
            // Fetch transactions for the address
            const response = await fetch(
              `${config.electrsUrl}/address/${sourceAddress}/txs`,
              {
                headers: {
                  Authorization:
                    "Basic " +
                    Buffer.from("spark-sdk:mCMk1JqlBNtetUNy").toString(
                      "base64",
                    ),
                },
              },
            );

            const transactions: any = await response.json();

            // Find unspent outputs
            const utxos: { txid: string; vout: number; value: bigint }[] = [];
            for (const tx of transactions) {
              for (let voutIndex = 0; voutIndex < tx.vout.length; voutIndex++) {
                const output = tx.vout[voutIndex];
                if (output.scriptpubkey_address === sourceAddress) {
                  const isSpent = transactions.some((otherTx: any) =>
                    otherTx.vin.some(
                      (input: any) =>
                        input.txid === tx.txid && input.vout === voutIndex,
                    ),
                  );

                  if (!isSpent) {
                    utxos.push({
                      txid: tx.txid,
                      vout: voutIndex,
                      value: BigInt(output.value),
                    });
                  }
                }
              }
            }

            if (utxos.length === 0) {
              console.log(
                `No unspent outputs found. Please fund the address ${sourceAddress} first`,
              );
              break;
            }

            // Create unsigned transaction
            const tx = new Transaction();

            const sendAmount = 10000n; // 10000 sats
            const utxo = utxos[0];

            // Add input without signing
            tx.addInput({
              txid: utxo.txid,
              index: utxo.vout,
              witnessUtxo: {
                script: getP2TRScriptFromPublicKey(
                  secp256k1.getPublicKey(hexToBytes(privateKey)),
                  Network.REGTEST,
                ),
                amount: utxo.value,
              },
              tapInternalKey: schnorr.getPublicKey(hexToBytes(privateKey)),
            });

            // Add output for destination
            const destinationAddress = Address(
              getNetwork(Network.REGTEST),
            ).decode(args[0]);
            const desitnationScript = OutScript.encode(destinationAddress);
            tx.addOutput({
              script: desitnationScript,
              amount: sendAmount,
            });

            // Get unsigned transaction hex
            // Initialize deposit with unsigned transaction
            console.log("Initializing deposit with unsigned transaction...");
            const depositResult = await wallet.advancedDeposit(tx.hex);
            console.log("Deposit initialization result:", depositResult);

            // Now sign the transaction
            console.log("Signing transaction...");
            tx.sign(hexToBytes(privateKey));
            tx.finalize();

            const signedTxHex = hex.encode(tx.extract());

            // Broadcast the signed transaction
            const broadcastResponse = await fetch(`${config.electrsUrl}/tx`, {
              method: "POST",
              headers: {
                Authorization:
                  "Basic " +
                  Buffer.from("spark-sdk:mCMk1JqlBNtetUNy").toString("base64"),
                "Content-Type": "text/plain",
              },
              body: signedTxHex,
            });

            if (!broadcastResponse.ok) {
              const error = await broadcastResponse.text();
              throw new Error(`Failed to broadcast transaction: ${error}`);
            }

            const txid = await broadcastResponse.text();
            console.log("Transaction broadcast successful!", txid);
          } catch (error: any) {
            console.error("Error creating deposit:", error);
            console.error("Error details:", error.message);
          }
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

          await new Promise((resolve) => setTimeout(resolve, 1000));

          console.log(depositResult);
          break;
        case "gettransfers":
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const limit = args[0] ? parseInt(args[0]) : 10;
          const offset = args[1] ? parseInt(args[1]) : 0;
          if (isNaN(limit) || isNaN(offset)) {
            console.log("Invalid limit or offset");
            break;
          }
          if (limit < 0 || offset < 0) {
            console.log("Limit and offset must be non-negative");
            break;
          }
          const transfers = await wallet.getTransfers(limit, offset);
          console.log(transfers);
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
          const lightningReceiveRequest =
            await wallet.getLightningReceiveRequest(args[0]);
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
        case "initwallet":
          if (wallet) {
            wallet.cleanupConnections();
          }
          let mnemonicOrSeed;
          let accountNumber;
          if (args.length == 13) {
            mnemonicOrSeed = args.slice(0, -1).join(" ");
            accountNumber = parseInt(args[args.length - 1]);
          } else if (args.length == 12) {
            mnemonicOrSeed = args.join(" ");
          } else if (args.length !== 0) {
            console.log(
              "Invalid number of arguments - usage: initwallet [mnemonic | seed] [accountNumber (optional)]",
            );
            break;
          }
          let options: ConfigOptions = {
            ...config,
            network,
          };
          try {
            const { wallet: newWallet, mnemonic: newMnemonic } =
              await IssuerSparkWallet.initialize({
                mnemonicOrSeed,
                options,
                accountNumber,
              });
            wallet = newWallet;
            console.log("Mnemonic:", newMnemonic);
            console.log("Network:", options.network);
            wallet.on(
              "deposit:confirmed",
              (depositId: string, balance: number) => {
                console.log(
                  `Deposit ${depositId} marked as available. New balance: ${balance}`,
                );
              },
            );

            wallet.on(
              "transfer:claimed",
              (transferId: string, balance: number) => {
                console.log(
                  `Transfer ${transferId} claimed. New balance: ${balance}`,
                );
              },
            );
            wallet.on("stream:connected", () => {
              console.log("Stream connected");
            });
            wallet.on(
              "stream:reconnecting",
              (
                attempt: number,
                maxAttempts: number,
                delayMs: number,
                error: string,
              ) => {
                console.log(
                  "Stream reconnecting",
                  attempt,
                  maxAttempts,
                  delayMs,
                  error,
                );
              },
            );
            wallet.on("stream:disconnected", (reason: string) => {
              console.log("Stream disconnected", reason);
            });
          } catch (error: any) {
            console.error("Error initializing wallet:", error);
            break;
          }
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
          const depositAddress = await wallet.getSingleUseDepositAddress();
          console.log(
            "WARNING: This is a single-use address, DO NOT deposit more than once or you will lose funds!",
          );
          console.log(depositAddress);
          break;
        case "identity":
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const identity = await wallet.getIdentityPublicKey();
          console.log(identity);
          break;
        case "getsparkaddress":
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const sparkAddress = await wallet.getSparkAddress();
          console.log(sparkAddress);
          break;
        case "decodesparkaddress":
          if (args.length !== 2) {
            console.log(
              "Usage: decodesparkaddress <sparkAddress> <network> (mainnet, regtest, testnet, signet, local)",
            );
            break;
          }

          const decodedAddress = decodeSparkAddress(
            args[0],
            args[1].toUpperCase() as NetworkType,
          );
          console.log(decodedAddress);
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
          let maxFeeSats = parseInt(args[1]);
          if (isNaN(maxFeeSats)) {
            console.log("Invalid maxFeeSats value");
            break;
          }
          const payment = await wallet.payLightningInvoice({
            invoice: args[0],
            maxFeeSats: maxFeeSats,
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
        case "transfertokens":
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          if (args.length < 3) {
            console.log(
              "Usage: transfertokens <tokenPubKey> <amount> <receiverPubKey>",
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
            console.log("Transfer Transaction ID:", result);
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
            exitSpeed: args[2].toUpperCase() as ExitSpeed,
          });
          console.log(withdrawal);
          break;
        case "withdrawalfee": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const fee = await wallet.getWithdrawalFeeEstimate({
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
        case "gettokenl1address": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const l1Address = await wallet.getTokenL1Address();
          console.log(l1Address);
          break;
        }
        case "getissuertokenbalance": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const balance = await wallet.getIssuerTokenBalance();
          console.log("Issuer Token Balance:", balance.balance.toString());
          break;
        }
        case "getissuertokeninfo": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const info = await wallet.getIssuerTokenInfo();
          if (info) {
            console.log("Token Info:", {
              tokenPublicKey: info.tokenPublicKey,
              tokenName: info.tokenName,
              tokenSymbol: info.tokenSymbol,
              tokenDecimals: info.tokenDecimals,
              maxSupply: info.maxSupply.toString(),
              isFreezable: info.isFreezable,
            });
          } else {
            console.log("No token info found");
          }
          break;
        }
        case "getissuertokenpublickey": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const pubKey = await wallet.getIdentityPublicKey();
          console.log("Issuer Token Public Key:", pubKey);
          break;
        }
        case "minttokens": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const amount = BigInt(parseInt(args[0]));
          const result = await wallet.mintTokens(amount);
          console.log("Mint Transaction ID:", result);
          break;
        }
        case "burntokens": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const amount = BigInt(parseInt(args[0]));
          const result = await wallet.burnTokens(amount);
          console.log("Burn Transaction ID:", result);
          break;
        }
        case "freezetokens": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const result = await wallet.freezeTokens(args[0]);
          console.log("Freeze Result:", {
            impactedOutputIds: result.impactedOutputIds,
            impactedTokenAmount: result.impactedTokenAmount.toString(),
          });
          break;
        }
        case "unfreezetokens": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const result = await wallet.unfreezeTokens(args[0]);
          console.log("Unfreeze Result:", {
            impactedOutputIds: result.impactedOutputIds,
            impactedTokenAmount: result.impactedTokenAmount.toString(),
          });
          break;
        }
        case "getissuertokenactivity": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          const result = await wallet.getIssuerTokenActivity();
          if (!result.transactions || result.transactions.length === 0) {
            console.log("No transactions found");
          }
          for (const transaction of result.transactions) {
            console.log(
              `Token Activity - case: ${transaction.transaction?.$case} | operation type: ${transaction.transaction?.$case === "spark" ? transaction.transaction?.spark.operationType : transaction.transaction?.onChain.operationType}`,
            );
          }
          break;
        }
        case "announcetoken": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          if (args.length < 5) {
            console.log(
              "Usage: announcetoken <tokenName> <tokenTicker> <decimals> <maxSupply> <isFreezable>",
            );
            break;
          }
          const [tokenName, tokenTicker, decimals, maxSupply, isFreezable] =
            args;
          const result = await wallet.announceTokenL1(
            tokenName,
            tokenTicker,
            parseInt(decimals),
            BigInt(maxSupply),
            isFreezable.toLowerCase() === "true",
          );
          console.log("Token Announcement Transaction ID:", result);
          break;
        }
        case "querytokentransactions": {
          if (!wallet) {
            console.log("Please initialize a wallet first");
            break;
          }
          if (args.length > 2) {
            console.log(
              "Usage: querytokentransactions [tokenPublicKey] [tokenTransactionHash]",
            );
            break;
          }

          try {
            let tokenPublicKeys: string[];
            if (args.length === 0) {
              // If no token public key is provided, use the wallet's own public key
              const publicKey = await wallet.getIdentityPublicKey();
              tokenPublicKeys = [publicKey];
            } else {
              tokenPublicKeys = [args[0]];
            }

            const tokenTransactionHashes = args[1] ? [args[1]] : undefined;

            const transactions = await wallet.queryTokenTransactions(
              tokenPublicKeys,
              tokenTransactionHashes,
            );
            console.log("\nToken Transactions:");
            for (const tx of transactions) {
              console.log("\nTransaction Details:");
              console.log(`  Status: ${TokenTransactionStatus[tx.status]}`);

              if (tx.tokenTransaction?.tokenInputs) {
                const input = tx.tokenTransaction.tokenInputs;
                if (input.$case === "mintInput") {
                  console.log("  Type: Mint");
                  console.log(
                    `  Issuer Public Key: ${hex.encode(input.mintInput.issuerPublicKey)}`,
                  );
                  console.log(
                    `  Timestamp: ${new Date(input.mintInput.issuerProvidedTimestamp * 1000).toISOString()}`,
                  );
                } else if (input.$case === "transferInput") {
                  console.log("  Type: Transfer");
                  console.log(
                    `  Outputs to Spend: ${input.transferInput.outputsToSpend.length}`,
                  );
                }
              }

              if (tx.tokenTransaction?.tokenOutputs) {
                console.log("\n  Outputs:");
                for (const output of tx.tokenTransaction.tokenOutputs) {
                  console.log(
                    `    Owner Public Key: ${hex.encode(output.ownerPublicKey)}`,
                  );
                  console.log(
                    `    Token Public Key: ${hex.encode(output.tokenPublicKey)}`,
                  );
                  console.log(
                    `    Token Amount: ${hex.encode(output.tokenAmount)}`,
                  );
                  if (output.withdrawBondSats !== undefined) {
                    console.log(
                      `    Withdraw Bond Sats: ${output.withdrawBondSats}`,
                    );
                  }
                  if (output.withdrawRelativeBlockLocktime !== undefined) {
                    console.log(
                      `    Withdraw Relative Block Locktime: ${output.withdrawRelativeBlockLocktime}`,
                    );
                  }
                  console.log("    ---");
                }
              }
              console.log("----------------------------------------");
            }
          } catch (error) {
            console.error("Error querying token transactions:", error);
          }
          break;
        }
      }
    } catch (error) {
      console.error("Error:", error);
    }
  }
}

runCLI();
