# Spark Hackathon Node Server

Welcome to the Spark Hackathon!

Contained is a simple express server example written in plain javascript that calls most of our exposed sdk functions.

To get started:

```
yarn
yarn dev
```

or if you'd like you can copy this directory as a starting template for your own project and use a different package manager like npm:

```
npm install
npm run dev
```

To init a new wallet, make an empty POST request to either.

```
https://localhost:{PORT}/spark-wallet/init
```

or

```
https://localhost:{PORT}/issuer-wallet/init
```

Your mnemonic should then get saved to your local machine and you can explore our api from there.

**To spin up a new wallet, delete the saved mnemonic files.**

If your server crashes, remember to init your wallet again.

You can find more documentation for our sdks at https://docs.spark.info

## Bitcoin Network configuration

To change the bitcoin network, update the `BITCOIN_NETWORK` variable in [`./src/index.ts`](./src/index.ts#L13).

## Postman Collection for testing:

You can import the below collection into postman to test the endpoints in dev.

[Spark SDK API](./spark-sdk-api.postman_collection.json)

## Methods available to both spark and issuer wallets

As an `IssuerSparkWallet` extends the functionality of a `SparkWallet`, `IssuerSparkWallet`s have access to all the methods available in a `SparkWallet`.

### Get Wallet

Returns the raw wallet instance.

```http
GET /spark-wallet/wallet
GET /issuer-wallet/wallet
```

---

### Initialize Wallet

Initialize a new wallet or recovers an existing one.

```http
POST /spark-wallet/wallet/init
POST /issuer-wallet/wallet/init
```

**Request Body:**

```json
{
  mnemonicOrSeed?: string | undefined
}
```

If no mnemonic is provided, generates a new one and saves it.

---

### Get Identity Public Key

Returns the wallet's identity public key.

```http
GET /spark-wallet/wallet/identity-public-key
GET /issuer-wallet/wallet/identity-public-key
```

---

### Get Spark Address

Returns the wallet's Spark address.

```http
GET /spark-wallet/wallet/spark-address
GET /issuer-wallet/wallet/spark-address
```

---

### Get Wallet Balance

Returns the current wallet balance, including token balances.

```http
GET /spark-wallet/wallet/balance
GET /issuer-wallet/wallet/balance
```

---

### Get Transfer History

Returns a list of transfers.

```http
GET /spark-wallet/wallet/transfers?limit=20&offset=0
GET /issuer-wallet/wallet/transfers?limit=20&offset=0
```

**Query Parameters:**

- `limit` (optional Number): Number of transfers to return (default: 20)
- `offset` (optional Number): Offset for pagination (default: 0)

---

### Get Pending Transfers

Returns a list of pending transfers.

```http
GET /spark-wallet/wallet/pending-transfers
GET /issuer-wallet/wallet/pending-transfers
```

### Claim Pending Transfers

Claim all pending transfers.

```http
POST /spark-wallet/wallet/claim-transfers
POST /issuer-wallet/wallet/claim-transfers
```

---

### Send Spark Transfer

Send a Spark transfer to another address.

```http
POST /spark-wallet/spark/send-transfer
POST /issuer-wallet/spark/send-transfer
```

**Request Body:**

```json
{
  receiverSparkAddress: string,
  amountSats: number
}
```

---

### Create Lightning Invoice

Generate a new Lightning Network invoice.

```http
POST /spark-wallet/lightning/create-invoice
POST /issuer-wallet/lightning/create-invoice
```

**Request Body:**

```json
{
  amountSats: number,
  memo?: string | undefined,
  expirySeconds?: number | undefined
}
```

---

### Pay Lightning Invoice

Pay a Lightning Network invoice.

```http
POST /spark-wallet/lightning/pay-invoice
POST /issuer-wallet/lightning/pay-invoice
```

**Request Body:**

```json
{
  invoice: string
}
```

---

### Get Deposit Address

Generate a Bitcoin deposit address associated with the current wallet.

<span style="color: red;">**IMPORTANT: Deposits made to this address will NOT work for Token Announcements on L1.**</span>

```http
GET /spark-wallet/bitcoin/deposit-address
GET /issuer-wallet/bitcoin/deposit-address
```

---

### Claim Deposit

Claim a Bitcoin deposit.

```http
POST /spark-wallet/bitcoin/claim-deposit
POST /issuer-wallet/bitcoin/claim-deposit
```

**Request Body:**

```json
{
  txid: string
}
```

---

### Withdraw to Bitcoin Address

Withdraw funds to a Bitcoin address.

```http
POST /spark-wallet/bitcoin/withdraw
POST /issuer-wallet/bitcoin/withdraw
```

**Request Body:**

```json
{
  onchainAddress: string,
  targetAmountSats?: number | undefined
}
```

---

### Transfer Tokens

Transfer tokens to another address.

```http
POST /spark-wallet/tokens/transfer
POST /issuer-wallet/tokens/transfer
```

**Request Body:**

```json
{
  tokenPublicKey: string,
  tokenAmount: number,
  receiverSparkAddress: string
}
```

---

### Withdraw Tokens

Withdraw tokens.

```http
POST /spark-wallet/tokens/withdraw
POST /issuer-wallet/tokens/withdraw
```

**Request Body:**

```json
{
  tokenPublicKey: string,
  receiverPublicKey?: string | undefined,
  leafIds?: string[] | undefined
}
```

## Issuer only methods

These endpoints are exclusively available for issuer wallets (`/issuer-wallet/...`).

### Get Token Balance

Returns the issuer's token balance.

```http
GET /issuer-wallet/token-balance
```

### Get Token Public Key Info

Returns information about the token's public key.

```http
GET /issuer-wallet/token-public-key-info
```

---

### Get Token Activity

Returns a list of all token transactions.

```http
GET /issuer-wallet/token-activity
```

**Query Parameters:**

- `pageSize` (optional Number): Number of transactions to return (default: 20)
- `lastTransactionHash` (optional String): The hash of the last transaction as a hex string
- `layer` (optional String): The layer of the last transaction "L1" or "SPARK"

---

### Get Issuer Token Activity

Returns a list of all issuer token transactions.

```http
GET /issuer-wallet/issuer-token-activity
```

**Query Parameters:**

- `pageSize` (optional Number): Number of transactions to return (default: 20)
- `lastTransactionHash` (optional String): The hash of the last transaction as a hex string
- `layer` (optional String): The layer of the last transaction "L1" or "SPARK"

---

### Mint Tokens

Mint new tokens.

```http
POST /issuer-wallet/spark/mint-tokens
```

**Request Body:**

```json
{
  tokenAmount: string // Amount to mint (will be converted to BigInt)
}
```

---

### Burn Tokens

Burn existing tokens.

```http
POST /issuer-wallet/spark/burn-tokens
```

**Request Body:**

```json
{
  tokenAmount: string // Amount to burn (will be converted to BigInt)
}
```

---

### Freeze Tokens

Freeze tokens for a specific owner.

```http
POST /issuer-wallet/spark/freeze-tokens
```

**Request Body:**

```json
{
  ownerPublicKey: string
}
```

---

### Unfreeze Tokens

Unfreeze tokens for a specific owner.

```http
POST /issuer-wallet/spark/unfreeze-tokens
```

**Request Body:**

```json
{
  ownerPublicKey: string
}
```

---

### On-Chain Operations

#### Get L1 Address

Returns the L1 address of the issuer wallet.

You MUST deposit to this address before announcing to L1.

<span style="color: red;">**IMPORTANT: The L1 address generated by the Spark Wallet from GET /bitcoin/deposit-address will NOT work for Token Announcements on L1.**</span>

```http
GET /issuer-wallet/on-chain/issuer-address
```

---

#### Announce Token L1

Announce a new token on Layer 1.

```http
POST /issuer-wallet/on-chain/announce-token
```

**Request Body:**

```json
{
  tokenName: string,
  tokenTicker: string,
  decimals: number,
  maxSupply: number,
  isFreezable: boolean,
  feeRateSatsPerVb?: number | undefined
}
```

---

#### Mint Tokens L1

Mint tokens on Layer 1.

```http
POST /issuer-wallet/on-chain/mint-tokens
```

**Request Body:**

```json
{
  tokenAmount: number
}
```

---

#### Transfer Tokens L1

Transfer tokens on Layer 1.

```http
POST /issuer-wallet/on-chain/transfer-tokens
```

**Request Body:**

```json
{
  tokenAmount: number,
  receiverPublicKey: string
}
```
