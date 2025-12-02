import { bech32m } from "@scure/base";

import { NetworkType } from "../utils/network.js";
import { SparkValidationError } from "../errors/index.js";

const Bech32mTokenIdentifierTokenIdentifierNetworkPrefix: Record<
  NetworkType,
  string
> = {
  MAINNET: "btkn",
  REGTEST: "btknrt",
  TESTNET: "btknt",
  SIGNET: "btkns",
  LOCAL: "btknl",
} as const;

export type Bech32mTokenIdentifier =
  | `btkn1${string}`
  | `btknrt1${string}`
  | `btknt1${string}`
  | `btkns1${string}`
  | `btknl1${string}`;

export interface Bech32mTokenIdentifierData {
  tokenIdentifier: Uint8Array;
  network: NetworkType;
}

export function encodeBech32mTokenIdentifier(
  payload: Bech32mTokenIdentifierData,
): Bech32mTokenIdentifier {
  try {
    const words = bech32m.toWords(payload.tokenIdentifier);

    if (!payload.network) {
      throw new SparkValidationError(
        "Network is required to encode bech32m encoded token identifier",
        {
          field: "network",
          value: payload.network,
        },
      );
    }

    return bech32m.encode(
      Bech32mTokenIdentifierTokenIdentifierNetworkPrefix[payload.network!],
      words,
      500,
    ) as Bech32mTokenIdentifier;
  } catch (error) {
    throw new SparkValidationError(
      "Failed to encode bech32m encoded token identifier",
      {
        field: "tokenIdentifier",
        value: payload.tokenIdentifier,
      },
      error as Error,
    );
  }
}

export function decodeBech32mTokenIdentifier(
  bech32mTokenIdentifier: Bech32mTokenIdentifier,
  network?: NetworkType,
): Bech32mTokenIdentifierData {
  try {
    const decoded = bech32m.decode(
      bech32mTokenIdentifier as Bech32mTokenIdentifier,
      500,
    );

    if (
      network &&
      decoded.prefix !==
        Bech32mTokenIdentifierTokenIdentifierNetworkPrefix[network]
    ) {
      throw new SparkValidationError(
        "Invalid bech32m encoded token identifier prefix",
        {
          field: "bech32mTokenIdentifier",
          value: bech32mTokenIdentifier,
          expected: `prefix='${Bech32mTokenIdentifierTokenIdentifierNetworkPrefix[network]}'`,
        },
      );
    }

    const tokenIdentifier = bech32m.fromWords(decoded.words);

    return {
      tokenIdentifier,
      network:
        network ?? getNetworkFromBech32mTokenIdentifier(bech32mTokenIdentifier),
    };
  } catch (error) {
    if (error instanceof SparkValidationError) {
      throw error;
    }
    throw new SparkValidationError(
      "Failed to decode bech32m encoded token identifier",
      {
        field: "bech32mTokenIdentifier",
        value: bech32mTokenIdentifier,
      },
      error as Error,
    );
  }
}

export function getNetworkFromBech32mTokenIdentifier(
  bech32mTokenIdentifier: Bech32mTokenIdentifier,
): NetworkType {
  const separatorIndex = bech32mTokenIdentifier.indexOf("1");
  if (separatorIndex === -1) {
    throw new SparkValidationError(
      "Invalid bech32m token identifier: no separator found",
      {
        field: "bech32mTokenIdentifier",
        value: bech32mTokenIdentifier,
      },
    );
  }

  const prefix = bech32mTokenIdentifier.slice(0, separatorIndex);

  for (const [network, networkPrefix] of Object.entries(
    Bech32mTokenIdentifierTokenIdentifierNetworkPrefix,
  )) {
    if (networkPrefix === prefix) {
      return network as NetworkType;
    }
  }

  throw new SparkValidationError("Unknown bech32m token identifier prefix", {
    field: "bech32mTokenIdentifier",
    value: bech32mTokenIdentifier,
  });
}
