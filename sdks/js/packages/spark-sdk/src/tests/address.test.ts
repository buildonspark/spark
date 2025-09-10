import { uuidv7obj } from "uuidv7";

import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
  numberToVarBytesBE,
} from "@noble/curves/utils";
import { bech32m } from "@scure/base";
import { SparkAddress } from "../proto/spark.js";
import {
  bech32mDecode,
  decodeSparkAddress,
  encodeSparkAddress,
  encodeSparkAddressWithSignature,
  getNetworkFromSparkAddress,
  SparkAddressData,
  SparkAddressFormat,
} from "../utils/address.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
} from "../utils/token-identifier.js";

describe("Spark Invoice Encode/Decode", () => {
  const testCases = [
    {
      name: "no empty fields",
      emptyAmount: false,
      overMaxSatsAmount: false,
      emptyMemo: false,
      emptyExpiryTime: false,
      emptySenderPublicKey: false,
      emptyIdentityPublicKey: false,
      invalidVersion: false,
      invalidId: false,
    },
    {
      name: "empty amount",
      emptyAmount: true,
    },
    {
      name: "empty amount",
      overMaxSatsAmount: true,
    },
    {
      name: "empty memo",
      emptyMemo: true,
    },
    {
      name: "empty expiry time",
      emptyExpiryTime: true,
    },
    {
      name: "empty sender public key",
      emptySenderPublicKey: true,
    },
    {
      name: "empty identity public key",
      emptyIdentityPublicKey: true,
    },
    {
      name: "invalid version",
      invalidVersion: true,
    },
    {
      name: "invalid id",
      invalidId: true,
    },
  ];

  testCases.forEach((tc) => {
    test(tc.name, async () => {
      let identityPublicKey: string | undefined =
        "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";
      let senderPublicKey: string | undefined = identityPublicKey;

      const testUUID: Uint8Array = uuidv7obj().bytes;
      let tokenIdentifier: Bech32mTokenIdentifier =
        "btknrt1kcsyuqlkqz48f7pg442xefz3u355ccnn8v55keaz6hav42m032gs5nly6r";
      let satsAmount: number | undefined = 1000;
      let tokenAmount: Uint8Array | undefined = numberToVarBytesBE(1000n);
      let expiryTime = new Date(Date.now() + 24 * 60 * 60 * 1000);
      let memo = "myMemo";

      const rawTokenIdentifier = decodeBech32mTokenIdentifier(
        tokenIdentifier,
        "REGTEST",
      ).tokenIdentifier;

      if (tc.emptyAmount) {
        tokenAmount = undefined;
        satsAmount = undefined;
      }
      if (tc.overMaxSatsAmount) {
        satsAmount = 2_100_000_000_000_001;
      }
      if (tc.emptyMemo) {
        memo = "";
      }
      if (tc.emptySenderPublicKey) {
        senderPublicKey = undefined;
      }
      if (tc.emptyIdentityPublicKey) {
        identityPublicKey = undefined;
      }

      const tokenInvoiceFields = {
        version: tc.invalidVersion ? 9999 : 1,
        id: tc.invalidId ? new Uint8Array([1, 2, 3]) : testUUID,
        paymentType: {
          $case: "tokensPayment" as const,
          tokensPayment: {
            tokenIdentifier: rawTokenIdentifier,
            amount: tc.emptyAmount ? undefined : tokenAmount,
          },
        },
        memo: tc.emptyMemo ? undefined : memo,
        senderPublicKey: tc.emptySenderPublicKey
          ? undefined
          : hexToBytes(senderPublicKey as string),
        expiryTime: tc.emptyExpiryTime ? undefined : expiryTime,
      };

      const satsInvoiceFields = {
        version: tc.invalidVersion ? 9999 : 1,
        id: tc.invalidId ? new Uint8Array([1, 2, 3]) : testUUID,
        paymentType: {
          $case: "satsPayment" as const,
          satsPayment: {
            amount: satsAmount,
          },
        },
        memo: tc.emptyMemo ? undefined : memo,
        senderPublicKey: tc.emptySenderPublicKey
          ? undefined
          : hexToBytes(senderPublicKey as string),
        expiryTime: tc.emptyExpiryTime ? undefined : expiryTime,
      } as const;

      const shouldFail =
        tc.invalidVersion ||
        tc.invalidId ||
        tc.emptyIdentityPublicKey ||
        tc.overMaxSatsAmount;

      const identityKey = tc.emptyIdentityPublicKey ? null : identityPublicKey;

      if (shouldFail) {
        if (!tc.overMaxSatsAmount) {
          // tokens should not fail on over max sats amount
          expect(() =>
            encodeSparkAddress({
              identityPublicKey: identityKey as string,
              network: "REGTEST",
              sparkInvoiceFields: tokenInvoiceFields,
            }),
          ).toThrow();
        }
        expect(() =>
          encodeSparkAddress({
            identityPublicKey: identityKey as string,
            network: "REGTEST",
            sparkInvoiceFields: satsInvoiceFields,
          }),
        ).toThrow();

        return;
      }

      // Encode addresses
      const tokensAddress = await encodeSparkAddress({
        identityPublicKey: identityKey as string,
        network: "REGTEST",
        sparkInvoiceFields: tokenInvoiceFields,
      });

      const satsAddress = await encodeSparkAddress({
        identityPublicKey: identityKey as string,
        network: "REGTEST",
        sparkInvoiceFields: satsInvoiceFields,
      });

      const decodedTokensAddress = decodeSparkAddress(tokensAddress, "REGTEST");
      const decodedSatsAddress = decodeSparkAddress(satsAddress, "REGTEST");

      expect(decodedTokensAddress.network).toBe("REGTEST");
      expect(decodedTokensAddress.identityPublicKey).toBe(identityPublicKey);
      expect(decodedTokensAddress.sparkInvoiceFields?.version).toBe(1);
      expect(decodedTokensAddress.sparkInvoiceFields?.memo).toBe(
        tc.emptyMemo ? undefined : memo,
      );

      if (
        decodedTokensAddress.sparkInvoiceFields?.paymentType?.type === "tokens"
      ) {
        expect(
          decodedTokensAddress.sparkInvoiceFields.paymentType.tokenIdentifier,
        ).toBe(bytesToHex(rawTokenIdentifier));
        if (!tc.emptyAmount) {
          expect(
            decodedTokensAddress.sparkInvoiceFields.paymentType.amount,
          ).toBe(bytesToNumberBE(tokenAmount as Uint8Array));
        } else {
          expect(
            decodedTokensAddress.sparkInvoiceFields.paymentType.amount,
          ).toBeUndefined();
        }
      }

      expect(decodedSatsAddress.network).toBe("REGTEST");
      expect(decodedSatsAddress.identityPublicKey).toBe(identityPublicKey);
      expect(decodedSatsAddress.sparkInvoiceFields?.version).toBe(1);
      expect(decodedSatsAddress.sparkInvoiceFields?.memo).toBe(
        tc.emptyMemo ? undefined : memo,
      );

      if (decodedSatsAddress.sparkInvoiceFields?.paymentType?.type === "sats") {
        expect(decodedSatsAddress.sparkInvoiceFields.paymentType.amount).toBe(
          satsAmount,
        );
      }
    });
  });
});

describe("getNetworkFromSparkAddress", () => {
  test("REGTEST", () => {
    const network = getNetworkFromSparkAddress(
      "sprt1pgssx63fa5g6uyv450rajp5ndwy9laxzpsp9e37su58jddmcdsvhgm5n7y0ud6",
    );
    expect(network).toBe("REGTEST");
  });
  test("MAINNET", () => {
    const network = getNetworkFromSparkAddress(
      "sp1pgssxwh6hznfdc3c0cuqrhgttder539d52a0rqcf34amge69huh664gd2ew787",
    );
    expect(network).toBe("MAINNET");
  });
});

describe("knownSparkAddress", () => {
  test("known spark address decodes and encodes to the same address", () => {
    const address =
      "sprt1pgss8stv8nfkamyea7mtc8werley55anfnnpgtnglff0wmxwm52mkyk6zfeqsqgjzqqe3dvr6e48l2alnpagf7ny3vlj5pr5v4ehgv3pqwd7wxx3awkku9p3epk73na6hcf9220h8kue2tmlkqx8tcrfpsf5ywsvpzgd9px9qcgvpzy8ecp35fg2yq4r39r4njq3slgcul7laarh9sndex9uejz7vwrcrz4g7n4egvwt5yspvsdyped46sflczvrzh0jzksgqnvaqlk02cz4vkwjrkwuep9zsrz5vmjp7mqxq7762tfjczy07at2fvzd7cgk2sqsxrmqdxnpy464rmq2nzdqzpuhme";
    const decoded = bech32mDecode(address as SparkAddressFormat);
    const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));

    const { identityPublicKey, sparkInvoiceFields, signature } = payload;

    const sparkAddressData: SparkAddressData = {
      identityPublicKey: bytesToHex(identityPublicKey),
      network: "REGTEST",
      sparkInvoiceFields: sparkInvoiceFields,
    };
    const reEncoded = encodeSparkAddressWithSignature(
      sparkAddressData,
      signature,
    );
    expect(reEncoded).toBe(address);
  });

  test("known spark address decodes to expected fields", () => {
    const address =
      "sprt1pgss8stv8nfkamyea7mtc8werley55anfnnpgtnglff0wmxwm52mkyk6zfeqsqgjzqqe3dvr6e48l2alnpagf7ny3vl35fg2yq4r39r4njq3slgcul7laarh9sndex9uejz7vwrcrz4g7n4egvwt5yspvs4qgar9wd6ryggrn0n3350t44hpgvwgdh5vlw47zf2jnaeahx2j7lasp367q6gvzdpr5rqgjrfgf3gxzrqg3p7wqvdyped46sflczvrzh0jzksgqnvaqlk02cz4vkwjrkwuep9zsrz5vmjp7mqxq7762tfjczy07at2fvzd7cgk2sqsxrmqdxnpy464rmq2nzdqneal34";

    const decoded = decodeSparkAddress(address, "REGTEST");

    expect(decoded.network).toBe("REGTEST");
    expect(decoded.identityPublicKey).toBe(
      "03c16c3cd36eec99efb6bc1dd91ff24a53b34ce6142e68fa52f76ccedd15bb12da",
    );

    const f = decoded.sparkInvoiceFields!;
    expect(f.version).toBe(1);
    expect(f.id).toBe("0198b583-d66a-7fab-bf98-7a84fa648b3f");

    expect(f.paymentType?.type).toBe("tokens");
    expect(
      f.paymentType && "tokenIdentifier" in f.paymentType
        ? f.paymentType.tokenIdentifier
        : undefined,
    ).toBe("2a3894759c81187d18e7fdfef4772c26dc98bccc85e6387818aa8f4eb9431cba");
    expect(
      f.paymentType && "amount" in f.paymentType
        ? f.paymentType.amount
        : undefined,
    ).toBe(100n);

    expect(f.memo).toBe("test");
    expect(f.senderPublicKey).toBe(
      "039be718d1ebad6e1431c86de8cfbabe125529f73db9952f7fb00c75e0690c1342",
    );

    expect(f.expiryTime?.toISOString()).toBe("2025-08-17T00:57:52.969Z");

    expect(decoded.signature).toBe(
      "e5b5d413fc098315df215a0804d9d07ecf56055659d21d9dcc84a280c5466e41f6c0607bda52d32c088ff756a4b04df61165401030f6069a61257551ec0a989a",
    );
  });
});
