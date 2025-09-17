const binding = require("../index.js");
const { test, imports } = require("./utils.js");
const { hexToBytes, bytesToHex } = require("@noble/hashes/utils", imports);

test("aggregateFrost error on invalid arguments", (assert) => {
  let error = null;
  try {
    binding.aggregateFrost(
      new Uint8Array([1, 2, 3]),
      [
        {
          secretKey: new Uint8Array([0]),
          publicKey: new Uint8Array([0]),
          verifyingKey: new Uint8Array([0]),
        },
      ],
      { hiding: new Uint8Array([0]), binding: new Uint8Array([0]) },
      { hiding: new Uint8Array([0]), binding: new Uint8Array([0]) },
      {},
      null,
    );
  } catch (e) {
    error = e;
  }
  assert(!!error, true, "aggregateFrost should error on invalid params");
});

test("aggregateFrost with valid params", (assert) => {
  const message = hexToBytes(
    "05454bd3d25b76a39d068adb14c37b33ffe8160816c26092626c828f87c0ffd0",
  );
  const selfCommitment = {
    hiding: hexToBytes(
      "0320e8527b032ea3dd63d23c8d4fd67fc5aa2105886f771b9cefb8c438402fa1c0",
    ),
    binding: hexToBytes(
      "030ee0590f12b0d8250f5c3663ea8302d2b545f96019ac31279c2e2677d8cbcacc",
    ),
  };
  const selfPublicKey = hexToBytes(
    "037433433c48a1a35688b687b0eb39c772e7f1b4e368feae4b5a33f075e46bb5f7",
  );
  const selfSignature = hexToBytes(
    "3f052b119fb2174d8c89761958c06506da91924b68e041c76f574aeb19e01b91",
  );
  const verifyingKey = hexToBytes(
    "02e5db919064ddb4807aca0898b2251e139ec18a9faff07e54125438a0faefc761",
  );
  const statechainCommitments = [
    [
      "0000000000000000000000000000000000000000000000000000000000000003",
      {
        hiding: hexToBytes(
          "03669678988f4e002412d0c8c37eb8fd4f2a30b8cefbd26f6b54163c4402dad300",
        ),
        binding: hexToBytes(
          "03e079bf59fc1026d1c04cb77c95e0313487bced814996357aefa977573e30412c",
        ),
      },
    ],
    [
      "0000000000000000000000000000000000000000000000000000000000000002",
      {
        hiding: hexToBytes(
          "035a0be8e0d551197e81d69229f07d1636c50fe3118610f61951c961353b568e2e",
        ),
        binding: hexToBytes(
          "03c9754ab6396358693a987c72e83b3d8b410c96e07e1b2e5a727be83eb3e7af79",
        ),
      },
    ],
  ];
  const statechainSignatures = [
    [
      "0000000000000000000000000000000000000000000000000000000000000003",
      hexToBytes(
        "ebcd40228211b67fb675e52fe6b2f222a122a59672c049482d46a3d415e5a88a",
      ),
    ],
    [
      "0000000000000000000000000000000000000000000000000000000000000002",
      hexToBytes(
        "934c283988e240f08b0484a30a48e464f2b1012375a7f143fb2664608b24413b",
      ),
    ],
  ];
  const statechainPublicKeys = [
    [
      "0000000000000000000000000000000000000000000000000000000000000003",
      hexToBytes(
        "03d09d62c1db20c8cb073a233d92d00e8eeec8e6b0e01004d0e3ee5ecfa58d4a0c",
      ),
    ],
    [
      "0000000000000000000000000000000000000000000000000000000000000002",
      hexToBytes(
        "025c9e7d0c3f2507903935850ca679a9ad213db6228593001a8f857f5f91fea4a4",
      ),
    ],
  ];

  const result = binding.aggregateFrost(
    message,
    statechainCommitments,
    selfCommitment,
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
    null,
  );
  const resultHex = bytesToHex(result);

  assert(
    resultHex,
    "a32847dcb81a35679512dcdfb9398d1786c18d08166e29e7f8247a0fb1a69711be1e936daaa60ebdce03dfec49bc3b8fb3b65c1ea1ffdc17d7f1f492eab3c415",
    "aggregateFrost result is correct",
  );
});
