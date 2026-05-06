import { secp256k1 } from "@noble/curves/secp256k1";
import { getP2TRAddressFromPublicKey, Network } from "../../index.node.js";

export async function getNewAddress(): Promise<string> {
  await Promise.resolve();
  const key = secp256k1.utils.randomPrivateKey();
  const pubKey = secp256k1.getPublicKey(key);
  return getP2TRAddressFromPublicKey(pubKey, Network.REGTEST);
}
