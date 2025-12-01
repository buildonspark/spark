# Spark SDK CLI

This is an interactive CLI with commands for interacting with the Spark SDK.

# Usage

Install dependencies by running `yarn`.
Run `yarn build` from sdks/js
Then run `yarn cli` to access the CLI. This CLI defaults to REGTEST.
For MAINNET: `yarn cli:mainnet`
These commands `yarn cli` or `yarn cli:XXX` will then display a list of available CLI commands & what they do.
Here is an example of a flow that initializes a Spark wallet, deposits funds to that wallet from the L1 faucet, and then transfers sats from the first wallet to a different wallet using Spark:

1. `initwallet`
2. `getdepositaddress`
3. Open the faucet website and paste in the deposit address you received in step (2). Press the 'Send Funds' button, this return you a transaction hash.
4. Go back to the cli and enter `claimdeposit XXX` where XXX is the transaction hash from step (3). You wallet is now funded! (this step may take a few seconds for the TX to be claimed, if it doesn't show up, reinitializing your wallet with mnemonic given in step 1 will re-run the claiming of the transfer)
5. Run `getbalance` to see your balance, run `getleaves` to see the details about all of the leaves that you own.
6. Open another terminal tab & run the cli again in whichever mainnet, regtest, dev environment that your first wallet was initialized in.
7. Init another wallet, get the spark address for this second wallet with `getsparkaddress`
8. Now go back to your first wallet and send a transfer to the second wallet with the command `sendtransfer XXX` where XXX is the spark address of your second wallet that you got in step (6)
9. Go back to your terminal tab with your second wallet and use the commands `getbalance` or `getleaves` to see that your transfer was received.

Regtest Faucet: https://app.lightspark.com/regtest-faucet
