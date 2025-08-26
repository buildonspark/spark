import { numberToBytesBE } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { Network } from "../proto/spark.js";
import {
  hashTokenTransactionV0,
  hashTokenTransactionV1,
  hashTokenTransactionV2,
} from "../utils/token-hashing.js";

// Test constants for consistent test data across all hash tests
const TEST_TOKEN_PUBLIC_KEY = new Uint8Array([
  242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50, 252,
  63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
]);

const TEST_IDENTITY_PUB_KEY = new Uint8Array([
  25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50, 252,
  63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_REVOCATION_PUB_KEY = new Uint8Array([
  100, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50, 252,
  63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_OPERATOR_PUB_KEY = new Uint8Array([
  200, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50, 252,
  63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_INVOICE_ATTACHMENTS = [
  {
    sparkInvoice:
      "sprt1pgssypkrjhrpzt2hw0ggrmndanmm035ley75nxu3gejaju4wx9nq86lwzfjqsqgjzqqe3zul2fm8a24y576t0ne2ehup5fg2yz4r6hxlhatyu9kpw09s2fk36ta5j0k85qascf6snpuy4sp0rp4ezyspvs4qgmt9d4hnyggzqmpet3s394th85ypaek7eaahc60uj02fnwg5vewew2hrzesra0hqflc0vn",
  },
  {
    sparkInvoice:
      "sprt1pgssypkrjhrpzt2hw0ggrmndanmm035ley75nxu3gejaju4wx9nq86lwzf5ssqgjzqqe3zulcs6h42v0kqkdsv9utxyp5fs2yz4r6hxlhatyu9kpw09s2fk36ta5j0k85qascf6snpuy4sp0rp4ezyszq86z5zryd9nxvmt9d4hnyggzqmpet3s394th85ypaek7eaahc60uj02fnwg5vewew2hrzesra0hql7r5ne",
  },
];

const TEST_LEAF_ID = "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436";
const TEST_BOND_SATS = 10000;
const TEST_LOCKTIME = 100;
const TEST_TOKEN_AMOUNT: bigint = 1000n;
const TEST_MAX_SUPPLY = new Uint8Array([
  0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3, 232,
]); // 1000 in BE format
const TEST_TOKEN_NAME = "TestToken";
const TEST_TOKEN_TICKER = "TEST";
const TEST_DECIMALS = 8;
const TEST_ISSUER_TIMESTAMP = 100;
const TEST_CLIENT_TIMESTAMP = 100;
const TEST_EXPIRY_TIME = 0;
const TEST_WITHDRAW_BOND_SATS = 10000;
const TEST_WITHDRAW_RELATIVE_BLOCK_LOCKTIME = 100;
const TEST_TOKEN_IDENTIFIER = new Uint8Array(32).fill(0x07);

// Precompute previous transaction hash to match Go test data
const PREV_TX_HASH = Uint8Array.from(
  sha256(new TextEncoder().encode("previous transaction")),
);

describe("hash token transaction", () => {
  it("should produce the exact same hash for mint v0 (legacy vector)", () => {
    const tokenAmount: bigint = 1000n;

    const tokenPublicKey = new Uint8Array([
      242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
    ]);

    const identityPubKey = new Uint8Array([
      25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
      252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
    ]);

    const tokenTransaction = {
      tokenInputs: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: tokenPublicKey,
          issuerProvidedTimestamp: 100,
        },
      },
      tokenOutputs: [
        {
          id: "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436",
          ownerPublicKey: identityPubKey,
          withdrawBondSats: 10000,
          withdrawRelativeBlockLocktime: 100,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          revocationCommitment: identityPubKey,
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
      network: Network.REGTEST,
    };

    const hash = hashTokenTransactionV0(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      66, 235, 134, 101, 172, 110, 147, 77, 122, 48, 86, 240, 239, 9, 163, 82,
      120, 234, 246, 206, 245, 242, 186, 180, 154, 41, 207, 179, 194, 31, 211,
      36,
    ]);
  });

  it("should produce the exact same hash for mint v0", () => {
    const tokenTransaction = {
      tokenInputs: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          issuerProvidedTimestamp: TEST_ISSUER_TIMESTAMP,
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
          withdrawBondSats: TEST_WITHDRAW_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_WITHDRAW_RELATIVE_BLOCK_LOCKTIME,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
    };

    const hash = hashTokenTransactionV0(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      56, 47, 132, 171, 2, 236, 10, 72, 214, 89, 28, 46, 171, 39, 221, 113, 162,
      74, 170, 64, 160, 91, 11, 201, 45, 35, 67, 179, 199, 130, 116, 69,
    ]);
  });

  it("should produce the exact same hash for create v0", () => {
    const tokenTransaction = {
      tokenInputs: {
        $case: "createInput" as const,
        createInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenName: TEST_TOKEN_NAME,
          tokenTicker: TEST_TOKEN_TICKER,
          decimals: TEST_DECIMALS,
          maxSupply: TEST_MAX_SUPPLY,
          isFreezable: false,
        },
      },
      tokenOutputs: [],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
    };

    const hash = hashTokenTransactionV0(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      35, 118, 177, 53, 49, 47, 174, 59, 123, 2, 212, 38, 217, 133, 124, 232,
      93, 185, 248, 87, 146, 123, 157, 10, 6, 111, 79, 183, 185, 175, 45, 224,
    ]);
  });

  it("should produce the exact same hash for transfer v0", () => {
    const tokenTransaction = {
      tokenInputs: {
        $case: "transferInput" as const,
        transferInput: {
          outputsToSpend: [
            {
              prevTokenTransactionHash: PREV_TX_HASH,
              prevTokenTransactionVout: 0,
            },
          ],
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(1000n, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
          withdrawBondSats: TEST_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_LOCKTIME,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
    };

    const hash = hashTokenTransactionV0(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      68, 88, 168, 87, 42, 251, 11, 182, 69, 202, 46, 202, 39, 234, 196, 201,
      24, 52, 213, 56, 151, 103, 99, 110, 211, 237, 148, 78, 216, 146, 143, 131,
    ]);
  });

  it("should produce the exact same hash for mint v1", () => {
    const tokenTransaction = {
      version: 1,
      tokenInputs: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenIdentifier: TEST_TOKEN_IDENTIFIER,
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          withdrawBondSats: TEST_WITHDRAW_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_WITHDRAW_RELATIVE_BLOCK_LOCKTIME,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV1(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      9, 162, 16, 177, 20, 91, 93, 148, 158, 249, 6, 42, 59, 136, 145, 184, 202,
      35, 243, 228, 14, 231, 132, 201, 66, 137, 201, 76, 97, 186, 149, 172,
    ]);
  });

  it("should produce the exact same hash for create v1", () => {
    const tokenTransaction = {
      version: 1,
      tokenInputs: {
        $case: "createInput" as const,
        createInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenName: TEST_TOKEN_NAME,
          tokenTicker: TEST_TOKEN_TICKER,
          decimals: TEST_DECIMALS,
          maxSupply: TEST_MAX_SUPPLY,
          isFreezable: false,
        },
      },
      tokenOutputs: [],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV1(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      201, 249, 88, 215, 6, 7, 221, 209, 103, 153, 36, 41, 19, 60, 80, 144, 153,
      159, 185, 61, 20, 117, 87, 196, 102, 151, 76, 4, 191, 121, 221, 182,
    ]);
  });

  it("should produce the exact same hash for transfer v1", () => {
    const tokenTransaction = {
      version: 1,
      tokenInputs: {
        $case: "transferInput" as const,
        transferInput: {
          outputsToSpend: [
            {
              prevTokenTransactionHash: PREV_TX_HASH,
              prevTokenTransactionVout: 0,
            },
          ],
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
          withdrawBondSats: TEST_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_LOCKTIME,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV1(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      86, 89, 220, 198, 197, 223, 236, 142, 73, 125, 112, 186, 29, 1, 26, 203,
      126, 154, 255, 176, 237, 210, 171, 98, 211, 130, 138, 113, 128, 129, 227,
      35,
    ]);
  });

  it("should produce the exact same hash for mint v2", () => {
    const tokenTransaction = {
      version: 2,
      tokenInputs: {
        $case: "mintInput" as const,
        mintInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenIdentifier: TEST_TOKEN_IDENTIFIER,
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          withdrawBondSats: TEST_WITHDRAW_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_WITHDRAW_RELATIVE_BLOCK_LOCKTIME,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV1(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      2, 4, 36, 141, 246, 170, 160, 204, 181, 102, 122, 220, 56, 182, 138, 153,
      199, 216, 80, 3, 35, 2, 146, 139, 209, 31, 195, 129, 121, 120, 236, 126,
    ]);
  });

  it("should produce the exact same hash for create v2", () => {
    const tokenTransaction = {
      version: 2,
      tokenInputs: {
        $case: "createInput" as const,
        createInput: {
          issuerPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenName: TEST_TOKEN_NAME,
          tokenTicker: TEST_TOKEN_TICKER,
          decimals: TEST_DECIMALS,
          maxSupply: TEST_MAX_SUPPLY,
          isFreezable: false,
        },
      },
      tokenOutputs: [],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV1(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      92, 161, 134, 55, 164, 211, 69, 97, 149, 43, 29, 110, 94, 225, 55, 59,
      178, 51, 203, 51, 189, 197, 203, 56, 6, 105, 55, 156, 106, 147, 155, 185,
    ]);
  });

  it("should produce the exact same hash for transfer v2", () => {
    const tokenTransaction = {
      version: 2,
      tokenInputs: {
        $case: "transferInput" as const,
        transferInput: {
          outputsToSpend: [
            {
              prevTokenTransactionHash: PREV_TX_HASH,
              prevTokenTransactionVout: 0,
            },
          ],
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
          withdrawBondSats: TEST_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_LOCKTIME,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: [],
    };

    const hash = hashTokenTransactionV2(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      21, 226, 190, 223, 0, 62, 121, 223, 94, 193, 34, 62, 186, 68, 52, 197, 6,
      189, 107, 37, 65, 141, 222, 109, 212, 128, 5, 40, 81, 247, 15, 249,
    ]);
  });

  it("should produce the exact same hash for transfer v2 with invoice attachments", () => {
    const tokenTransaction = {
      version: 2,
      tokenInputs: {
        $case: "transferInput" as const,
        transferInput: {
          outputsToSpend: [
            {
              prevTokenTransactionHash: PREV_TX_HASH,
              prevTokenTransactionVout: 0,
            },
          ],
        },
      },
      tokenOutputs: [
        {
          id: TEST_LEAF_ID,
          ownerPublicKey: TEST_IDENTITY_PUB_KEY,
          tokenPublicKey: TEST_TOKEN_PUBLIC_KEY,
          tokenAmount: numberToBytesBE(TEST_TOKEN_AMOUNT, 16),
          revocationCommitment: TEST_REVOCATION_PUB_KEY,
          withdrawBondSats: TEST_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_LOCKTIME,
        },
      ],
      sparkOperatorIdentityPublicKeys: [TEST_OPERATOR_PUB_KEY],
      network: Network.REGTEST,
      expiryTime: new Date(TEST_EXPIRY_TIME),
      clientCreatedTimestamp: new Date(TEST_CLIENT_TIMESTAMP),
      invoiceAttachments: TEST_INVOICE_ATTACHMENTS,
    };

    const hash = hashTokenTransactionV2(tokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      139, 4, 220, 112, 69, 32, 149, 81, 90, 67, 151, 101, 240, 182, 13, 123,
      70, 4, 153, 159, 172, 225, 15, 120, 71, 219, 154, 27, 72, 167, 2, 149,
    ]);
  });
});
