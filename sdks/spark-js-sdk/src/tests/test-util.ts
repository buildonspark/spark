import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { TreeNode } from "../proto/spark";
import {
  SigningOperator,
  WalletConfig,
  WalletConfigService,
} from "../services/config";
import { ConnectionManager } from "../services/connection";
import { DepositService } from "../services/deposit";
import { getTxFromRawTxBytes, getTxId } from "../utils/bitcoin";
import { createDummyTx } from "../utils/wasm";

export const TEST_WALLET_CONFIG = {
  network: "regtest",
  coodinatorIdentifier:
    "0000000000000000000000000000000000000000000000000000000000000001",
  frostSignerAddress: "unix:///tmp/frost_0.sock",
  threshold: 3,
  signingOperators: getAllSigningOperators(),
};

export function getAllSigningOperators(): Record<string, SigningOperator> {
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
      address: "localhost:8535",
      identityPublicKey: pubkeyBytesArray[0],
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "localhost:8536",
      identityPublicKey: pubkeyBytesArray[1],
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "localhost:8537",
      identityPublicKey: pubkeyBytesArray[2],
    },
    "0000000000000000000000000000000000000000000000000000000000000004": {
      id: 3,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000004",
      address: "localhost:8538",
      identityPublicKey: pubkeyBytesArray[3],
    },
    "0000000000000000000000000000000000000000000000000000000000000005": {
      id: 4,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000005",
      address: "localhost:8539",
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
    ...TEST_WALLET_CONFIG,
    identityPrivateKey,
  } as WalletConfig;
}

export async function createNewTree(
  config: WalletConfigService,
  privKey: Uint8Array,
  amountSats: bigint = 100_000n
): Promise<TreeNode> {
  const connectionManager = new ConnectionManager(config);
  const depositService = new DepositService(config, connectionManager);
  const mockClient = ConnectionManager.createMockClient(
    config.getCoordinatorAddress()
  );

  // Generate private/public key pair
  const pubKey = secp256k1.getPublicKey(privKey, true);

  // Generate deposit address
  const depositResp = await depositService.generateDepositAddress({
    signingPubkey: pubKey,
  });
  if (!depositResp.depositAddress) {
    throw new Error("deposit address not found");
  }

  const dummyTx = createDummyTx({
    address: depositResp.depositAddress.address,
    amountSats,
  });

  const depositTxHex = bytesToHex(dummyTx.tx);
  const depositTx = getTxFromRawTxBytes(dummyTx.tx);
  const vout = 0;
  const txid = getTxId(depositTx);
  if (!txid) {
    throw new Error("txid not found");
  }

  // Set mock transaction
  await mockClient.set_mock_onchain_tx({
    txid,
    tx: depositTxHex,
  });

  // Create tree root
  const treeResp = await depositService.createTreeRoot({
    signingPrivkey: privKey,
    verifyingKey: depositResp.depositAddress.verifyingKey,
    depositTx,
    vout,
  });

  mockClient.close();

  return treeResp.nodes[0];
}
