import { hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TreeNode } from "../proto/spark.js";
import {
  SigningOperator,
  WalletConfig,
  WalletConfigService,
} from "../services/config.js";
import { ConnectionManager } from "../services/connection.js";
import { DepositService } from "../services/deposit.js";
import { getP2TRAddressFromPublicKey } from "../utils/bitcoin.js";
import { getNetwork, Network } from "../utils/network.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

export const LOCAL_WALLET_CONFIG = {
  network: Network.REGTEST,
  coodinatorIdentifier:
    "0000000000000000000000000000000000000000000000000000000000000001",
  frostSignerAddress: "unix:///tmp/frost_0.sock",
  threshold: 3,
  signingOperators: getLocalSigningOperators(),
};

export const REGTEST_WALLET_CONFIG = {
  network: Network.REGTEST,
  coodinatorIdentifier:
    "0000000000000000000000000000000000000000000000000000000000000001",
  frostSignerAddress: "unix:///tmp/frost_0.sock",
  threshold: 3,
  signingOperators: getRegtestSigningOperators(),
};

export function getRegtestSigningOperators(): Record<string, SigningOperator> {
  const pubkeys = [
    "03acd9a5a88db102730ff83dee69d69088cc4c9d93bbee893e90fd5051b7da9651",
    "02d2d103cacb1d6355efeab27637c74484e2a7459e49110c3fe885210369782e23",
    "0350f07ffc21bfd59d31e0a7a600e2995273938444447cb9bc4c75b8a895dbb853",
  ];

  const pubkeyBytesArray = pubkeys.map((pubkey) => hexToBytes(pubkey));

  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://0.spark.dev.dev.sparkinfra.net",

      identityPublicKey: pubkeyBytesArray[0],
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://1.spark.dev.dev.sparkinfra.net",

      identityPublicKey: pubkeyBytesArray[1],
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://2.spark.dev.dev.sparkinfra.net",
      identityPublicKey: pubkeyBytesArray[2],
    },
  };
}

export function getLocalSigningOperators(): Record<string, SigningOperator> {
  const pubkeys = [
    "0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510",
    "0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27",
    "0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6",
    "0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea",
    "02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba",
  ];

  const pubkeyBytesArray = pubkeys.map((pubkey) => hexToBytes(pubkey));

  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://localhost:8535",
      identityPublicKey: pubkeyBytesArray[0],
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://localhost:8536",
      identityPublicKey: pubkeyBytesArray[1],
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://localhost:8537",
      identityPublicKey: pubkeyBytesArray[2],
    },
    "0000000000000000000000000000000000000000000000000000000000000004": {
      id: 3,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000004",
      address: "https://localhost:8538",
      identityPublicKey: pubkeyBytesArray[3],
    },
    "0000000000000000000000000000000000000000000000000000000000000005": {
      id: 4,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000005",
      address: "https://localhost:8539",
      identityPublicKey: pubkeyBytesArray[4],
    },
  };
}

export function getTestWalletConfig(): WalletConfig {
  const identityPrivateKey = secp256k1.utils.randomPrivateKey();
  return getTestWalletConfigWithIdentityKey(identityPrivateKey);
}

export function getTestWalletConfigWithIdentityKey(
  identityPrivateKey: Uint8Array
): WalletConfig {
  return {
    ...LOCAL_WALLET_CONFIG,
    identityPrivateKey,
  } as WalletConfig;
}

export async function createNewTree(
  wallet: SparkWalletTesting,
  pubKey: Uint8Array,
  faucet: BitcoinFaucet,
  amountSats: bigint = 100_000n
): Promise<TreeNode> {
  const faucetCoin = await faucet.fund();

  const configService = new WalletConfigService(
    Network.LOCAL,
    wallet.getSigner()
  );
  const connectionManager = new ConnectionManager(configService);
  const depositService = new DepositService(configService, connectionManager);

  const depositResp = await depositService.generateDepositAddress({
    signingPubkey: pubKey,
  });

  if (!depositResp.depositAddress) {
    throw new Error("deposit address not found");
  }

  const depositTx = new Transaction();
  depositTx.addInput(faucetCoin.outpoint);

  // Add the main output
  const addr = Address(getNetwork(Network.LOCAL)).decode(
    depositResp.depositAddress.address
  );
  const script = OutScript.encode(addr);
  depositTx.addOutput({ script, amount: amountSats });

  const treeResp = await depositService.createTreeRoot({
    signingPubKey: pubKey,
    verifyingKey: depositResp.depositAddress.verifyingKey,
    depositTx,
    vout: 0,
  });

  const signedDepositTx = await faucet.signFaucetCoin(
    depositTx,
    faucetCoin.txout,
    faucetCoin.key
  );

  await faucet.broadcastTx(signedDepositTx.hex);

  // Mine just 1 block instead of waiting for many confirmations
  const randomKey = secp256k1.utils.randomPrivateKey();
  const randomPubKey = secp256k1.getPublicKey(randomKey);
  const randomAddress = getP2TRAddressFromPublicKey(
    randomPubKey,
    Network.LOCAL
  );

  await faucet.generateToAddress(1, randomAddress);

  await new Promise((resolve) => setTimeout(resolve, 100));
  return treeResp.nodes[0];
}
