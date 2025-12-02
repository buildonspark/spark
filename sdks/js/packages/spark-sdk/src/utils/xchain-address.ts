import { encodeSparkAddress, SparkAddressFormat } from "./address.js";
import * as btc from "@scure/btc-signer";
import { SparkValidationError } from "../errors/index.js";

const networkByType = {
  MAINNET: btc.NETWORK,
  TESTNET: btc.TEST_NETWORK,
  REGTEST: {
    ...btc.TEST_NETWORK,
    bech32: "bcrt",
  },
} as const;

export function getSparkAddressFromTaproot(
  taprootAddress: string,
): SparkAddressFormat {
  for (const networkType of ["MAINNET", "TESTNET", "REGTEST"] as const) {
    try {
      const result = btc
        .Address(networkByType[networkType])
        .decode(taprootAddress);
      if (result.type === "tr") {
        const outputPublicKey = result.pubkey;
        return encodeSparkAddress({
          identityPublicKey: Buffer.concat([
            Buffer.from([0x02]),
            outputPublicKey,
          ]).toString("hex"),
          network: networkType,
        });
      }
    } catch (_) {}
  }

  throw new SparkValidationError("Invalid taproot address");
}
