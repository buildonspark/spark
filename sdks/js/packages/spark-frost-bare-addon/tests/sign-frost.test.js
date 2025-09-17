const binding = require("../index.js");
const { imports, test } = require("./utils.js");
const {
  hexToBytes,
  bytesToHex,
} = require("@noble/curves/abstract/utils", imports);

test("signFrost invalid arguments", (assert) => {
  let error = null;
  try {
    binding.signFrost(
      new Uint8Array([1, 2, 3]),
      {
        secretKey: new Uint8Array([0]),
        publicKey: new Uint8Array([0]),
        verifyingKey: new Uint8Array([0]),
      },
      { hiding: new Uint8Array([0]), binding: new Uint8Array([0]) },
      { hiding: new Uint8Array([0]), binding: new Uint8Array([0]) },
      {},
      null,
    );
  } catch (e) {
    error = e;
  }
  assert(!!error, true, "signFrost should error on invalid params");
});

test("signFrost with valid arguments", (assert) => {
  const message = hexToBytes(
    "309cfa947e5132b6a8dfec4f7c5a2b118f6a6339b29bb5f9eda67772cc4ca2ab",
  );
  const keyPackage = {
    secretKey: hexToBytes(
      "ea55351bebc990fb9e2c20f6e4172334c37c2ee644bd77830b2181da7dc4d991",
    ),
    publicKey: hexToBytes(
      "035e8f0057ae1d51ff2e88586bd5bf2e16bd87a3c4aac68945cbc96017e080e26e",
    ),
    verifyingKey: hexToBytes(
      "0265c8a4fd5613f89fbd88713ae6707d5bde332a35fc69bae105a10bbb93431480",
    ),
  };
  const nonce = {
    hiding: hexToBytes(
      "977e1e21064e00f0ca089e923fc303375fe370f7b62c954cd76cde5a6ef0dc9c",
    ),
    binding: hexToBytes(
      "1b1cce7906d81a968deebf5ba8c8e2e72d05bfebfdab899326fd5d97dd4e8aef",
    ),
  };
  const selfCommitment = {
    hiding: hexToBytes(
      "02e15ecb9c56f12ba55f47264f3dd21748b578dc5ffd26201547103e02fb281864",
    ),
    binding: hexToBytes(
      "035e38cdc33fd24dee735dc398dafd4d8c9e44da6aa45576b184786b517b8d61f1",
    ),
  };
  const statechainCommitments = [
    [
      "0000000000000000000000000000000000000000000000000000000000000003",
      {
        hiding: hexToBytes(
          "021cf1b3646f95cc6b2f8fd60290733b97bcafab8f0c513289c319bada58c5e01e",
        ),
        binding: hexToBytes(
          "03e9ba1827a469d925cc286f18a7cd1122bcd866f6263f8c49f0441f9d61226e32",
        ),
      },
    ],
    [
      "0000000000000000000000000000000000000000000000000000000000000002",
      {
        hiding: hexToBytes(
          "024acf3d72ce07efaf55f2229895faa936a9c8aa635198953096b7c30ad69492ea",
        ),
        binding: hexToBytes(
          "0259f706606ecf5ef4fa02f5109c1e498c75b4c679d3410e6248a343bdf6419921",
        ),
      },
    ],
  ];
  const adaptorPubKey = null;

  const result = binding.signFrost(
    message,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  );
  const hexResult = bytesToHex(result);
  assert(
    hexResult,
    "2ee25c78d61fc3ae8e4c91059369f23fd7a04ea54a43afe1f681276a063659e2",
    "signFrost output matches",
  );
});
