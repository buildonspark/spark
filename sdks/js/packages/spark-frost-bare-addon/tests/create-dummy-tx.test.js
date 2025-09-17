const binding = require("../index.js");
const { imports, test } = require("./utils.js");
const secp256k1 = require("@noble/secp256k1", imports);
const {
  hexToBytes,
  bytesToHex,
} = require("@noble/curves/abstract/utils", imports);

function log(msg, ...args) {
  console.log(`index.js: ${msg}`, ...args);
}

test("createDummyTx valid address and amount", (assert) => {
  const dummyTx = binding.createDummyTx(
    "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
    10000n,
  );
  const txHex = bytesToHex(dummyTx.tx);

  assert(
    dummyTx.txid,
    "bc39a4e94553598d19fe6f31d0f98fc4babbd993afac959447499d8364f63b32",
    "txid is correct",
  );
  assert(
    txHex,
    "020000000100000000000000000000000000000000000000000000000000000000000000000000000000000000000110270000000000001600149f09996f6afd6dd640207c81d73d2c7fbe8d099100000000",
    "tx is correct",
  );
});

test("createDummyTx invalid address", (assert) => {
  let err = null;
  try {
    log('binding.createDummyTx("this_address_will_error", 10000n):');
    const dummyTx = binding.createDummyTx("this_address_will_error", 10000n);
    log("dummyTx", dummyTx);
    tx = dummyTx;
  } catch (e) {
    err = e;
  }

  assert(
    err,
    "failed to create dummy tx: invalid address: base58 error",
    "error message is correct",
  );
});

test("createDummyTx missing amount argument", (assert) => {
  let err = null;
  try {
    const dummyTx = binding.createDummyTx(
      "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
    );
    log("dummyTx", dummyTx);
  } catch (e) {
    log("error obj:", e);
    err = e;
  }

  assert(
    err,
    "amountSats argument missing or not a bigint",
    "error message is correct",
  );
});
