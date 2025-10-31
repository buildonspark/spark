import { numberToBytesBE } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { Network } from "../proto/spark.js";
import {
  hashTokenTransactionV1,
  hashTokenTransactionV2,
} from "../utils/token-hashing.js";

// Test constants for consistent test data across all hash tests - matching Go test data
const TEST_TOKEN_PUBLIC_KEY = new Uint8Array([
  0x02, 242, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123,
  50, 252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 45,
]);

const TEST_IDENTITY_PUB_KEY = new Uint8Array([
  0x02, 25, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123, 50,
  252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_REVOCATION_PUB_KEY = new Uint8Array([
  0x02, 100, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123,
  50, 252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_OPERATOR_PUB_KEY = new Uint8Array([
  0x02, 200, 155, 208, 90, 72, 211, 120, 244, 69, 99, 28, 101, 149, 222, 123,
  50, 252, 63, 99, 54, 137, 226, 7, 224, 163, 122, 93, 248, 42, 159, 173, 46,
]);

const TEST_INVOICE_ATTACHMENTS = [
  {
    sparkInvoice:
      "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmssqgjzqqejtaxmwj8ms9rn58574nvlq4j5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5rqgandgr3sxzrqdmew8qydzvz3qpylysylkgcaw9vpm2jzspls0qtr5kfmlwz244rvuk25w5w2sgc2pyqsraqdyp8tf57a6cn2egttaas9ms3whssenmjqt8wag3lgyvdzjskfeupt8xwwdx4agxdm9f0wefzj28jmdxqeudwcwdj9vfl9sdr65x06r0tasf5fwz2",
  },
  {
    sparkInvoice:
      "sparkrt1pgssx5us3wkqjza8g80xz3a9gznx25msq6g3ty8exfym9q3ahcv86vsnzfmqsqgjzqqejtavuhf8n5uh9a74zw66kqaz5zr5v4ehgnt9d4hnyggr2wgghtqfpwn5rhnpg7j5pfn92dcqdyg4jrunyjdjsg7muxraxgfn5zcglrwcr3sxzzqt3wrjrgnq5gqf8eyp8ajx8t3tqw65s5q0urczca9jwlmsj4dgm89j4r4rj5zxzsfqyqlgrfqw9ucldgmfzs5zmkekj90thwzmn6ps55gdjnz23aarjkf245608yg0v2x6xdpdrz6m8xjlhtru0kygcu4zhqwlth9duadfqpruuzx4tc7fdckn",
  },
];

const TEST_LEAF_ID = "db1a4e48-0fc5-4f6c-8a80-d9d6c561a436";
const TEST_TOKEN_AMOUNT: bigint = 1000n;
const TEST_MAX_SUPPLY = numberToBytesBE(1000n, 16);
const TEST_TOKEN_NAME = "TestToken";
const TEST_TOKEN_TICKER = "TEST";
const TEST_DECIMALS = 8;
const TEST_ISSUER_TIMESTAMP = 100;
const TEST_CLIENT_TIMESTAMP = 100;
const TEST_EXPIRY_TIME = 0;
const TEST_BOND_SATS = 10000;
const TEST_LOCKTIME = 100;
const TEST_TOKEN_IDENTIFIER = new Uint8Array(32).fill(0x07);

// Precompute previous transaction hash to match Go test data
const PREV_TX_HASH = Uint8Array.from(
  sha256(new TextEncoder().encode("previous transaction")),
);

describe("Hash Token Transaction V1", () => {
  it("should produce the exact same hash for mint v1", () => {
    const partialTokenTransaction = {
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

    const hash = hashTokenTransactionV1(partialTokenTransaction, false);

    expect(Array.from(hash)).toEqual([
      0xfe, 0x93, 0x8b, 0x12, 0xbf, 0xed, 0x51, 0x79, 0xff, 0x29, 0x8d, 0x2e,
      0xd9, 0x66, 0x2b, 0x4a, 0xf6, 0xf8, 0x35, 0x18, 0x8f, 0x4e, 0xa4, 0xb1,
      0xb3, 0x3b, 0x61, 0x23, 0x14, 0x49, 0xdc, 0x81,
    ]);
  });

  it("should produce the exact same hash for create v1", () => {
    const createTransaction = {
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

    const hash = hashTokenTransactionV1(createTransaction, false);

    expect(Array.from(hash)).toEqual([
      0x04, 0x8a, 0xa2, 0xa0, 0x85, 0xab, 0xb9, 0xba, 0x96, 0x9c, 0x70, 0x7c,
      0x5f, 0xc7, 0xb3, 0xf2, 0x14, 0x8c, 0x89, 0x18, 0x5e, 0x0f, 0x7b, 0x16,
      0x17, 0xf8, 0xe8, 0x0d, 0x9e, 0x91, 0x48, 0x18,
    ]);
  });

  it("should produce the exact same hash for transfer v1", () => {
    const transferTransaction = {
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

    const hash = hashTokenTransactionV1(transferTransaction, false);

    expect(Array.from(hash)).toEqual([
      0xa9, 0xfa, 0xe6, 0x24, 0x05, 0xbb, 0x08, 0xe8, 0xa1, 0xf1, 0x6f, 0x9d,
      0xc8, 0xa5, 0x53, 0x03, 0xaf, 0x86, 0x6a, 0x67, 0x10, 0xb5, 0x50, 0x57,
      0xca, 0x0c, 0x8d, 0x64, 0x70, 0x00, 0xa5, 0x8f,
    ]);
  });
});

describe("Hash Token Transaction V2", () => {
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
          withdrawBondSats: TEST_BOND_SATS,
          withdrawRelativeBlockLocktime: TEST_LOCKTIME,
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
      129, 201, 149, 176, 132, 80, 18, 162, 211, 46, 171, 206, 83, 81, 0, 39,
      202, 90, 126, 100, 34, 60, 29, 219, 128, 93, 212, 58, 178, 181, 84, 183,
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
      209, 95, 96, 173, 113, 117, 99, 47, 242, 46, 135, 160, 99, 139, 26, 200,
      167, 236, 101, 218, 138, 171, 98, 117, 114, 118, 183, 206, 12, 106, 90,
      26,
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
      28, 151, 252, 16, 41, 53, 194, 50, 190, 167, 55, 2, 43, 179, 179, 255,
      117, 150, 148, 29, 158, 203, 107, 193, 82, 1, 77, 95, 41, 168, 208, 179,
    ]);
  });

  it("should produce the exact same hash for transfer v2 with invoice attachments", () => {
    const transferTransaction = {
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

    const hash = hashTokenTransactionV2(transferTransaction, false);

    expect(Array.from(hash)).toEqual([
      0xb0, 0x98, 0xdc, 0x22, 0x8a, 0x0d, 0x82, 0x64, 0x25, 0x4a, 0x2d, 0xef,
      0x34, 0x42, 0x5c, 0xab, 0xe2, 0x23, 0x0d, 0x4f, 0x7b, 0xa4, 0x3c, 0xf2,
      0xa3, 0x2c, 0x27, 0xf0, 0x31, 0xae, 0x08, 0x83,
    ]);
  });
});
