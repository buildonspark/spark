const binding = require("../index.js");
const { imports, test } = require("./utils.js");
const secp256k1 = require("@noble/secp256k1", imports);

test("encryptEcies returns ciphertext", (assert) => {
  const plaintext = new Uint8Array([1, 2, 3, 4]);
  // Compressed pubkey placeholder (33 bytes)
  const pk = new Uint8Array(33).fill(2);

  const cipher = binding.encryptEcies(plaintext, pk);
  assert(
    typeof cipher?.length === "number" && cipher.length > 0,
    true,
    "cipher has bytes",
  );
});

test("encryptEcies/decryptEcies roundtrip", (assert) => {
  const privKey = new Uint8Array(32).fill(1);
  const pubKey = secp256k1.getPublicKey(privKey, true);
  const pt = new Uint8Array([10, 11, 12]);

  const ct = binding.encryptEcies(pt, pubKey);
  const dec = binding.decryptEcies(ct, privKey);

  const ok = Buffer.from(dec).equals(Buffer.from(pt));
  assert(ok, true, "roundtrip decrypt matches original");
});
