import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import {
  DefaultSparkSigner,
  Network,
  SparkWallet,
  filterTokenBalanceForTokenIdentifier,
  getP2TRAddressFromPublicKey,
  getSparkFrost,
  getTxFromRawTxHex,
  signTransferManifest,
  type ConfigOptions,
  verifyTransferManifestSignature,
} from "@buildonspark/spark-sdk";
import { TransferManifest } from "@buildonspark/spark-sdk/proto/spark";
import {
  configureBrowserBitcoinRpcProxy,
  getExampleWalletOptions,
} from "../src/wallet-config.js";

const PARENT_TX_HEX =
  "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message);
  }
}

function hexToBytes(hex: string): Uint8Array {
  assert(hex.length % 2 === 0, "Expected an even-length hex string");
  assert(/^[0-9a-fA-F]*$/.test(hex), "Expected a hexadecimal string");
  return Uint8Array.from(
    hex.match(/.{2}/g)?.map((byte) => Number.parseInt(byte, 16)) ?? [],
  );
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(
    "",
  );
}

function equalBytes(left: Uint8Array, right: Uint8Array): boolean {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

function getFrostBindingMethods(frost: object): string[] {
  const methods = new Set<string>();
  let prototype = Object.getPrototypeOf(frost) as object | null;

  while (prototype && prototype !== Object.prototype) {
    for (const name of Object.getOwnPropertyNames(prototype)) {
      if (
        name !== "constructor" &&
        name !== "init" &&
        typeof Reflect.get(prototype, name) === "function"
      ) {
        methods.add(name);
      }
    }
    prototype = Object.getPrototypeOf(prototype) as object | null;
  }

  return [...methods].sort();
}

function recordMethodCalls(
  target: object,
  methods: string[],
): {
  calls: Set<string>;
  restore: () => void;
} {
  const calls = new Set<string>();
  const originals = new Map<string, PropertyDescriptor | undefined>();
  const mutableTarget = target as Record<string, unknown>;

  for (const method of methods) {
    const original = Reflect.get(target, method);
    assert(typeof original === "function", `${method} is not callable`);
    originals.set(method, Object.getOwnPropertyDescriptor(target, method));
    mutableTarget[method] = (...args: unknown[]) => {
      calls.add(method);
      return Reflect.apply(original, target, args);
    };
  }

  return {
    calls,
    restore: () => {
      for (const [method, descriptor] of originals) {
        if (descriptor) {
          Object.defineProperty(target, method, descriptor);
        } else {
          delete mutableTarget[method];
        }
      }
    },
  };
}

let activeFrostBindingRecorder:
  | ReturnType<typeof recordMethodCalls>
  | undefined;

export function startFrostBindingCallRecording(): string[] {
  activeFrostBindingRecorder?.restore();

  const frost = getSparkFrost();
  const methods = getFrostBindingMethods(frost);
  activeFrostBindingRecorder = recordMethodCalls(frost, methods);
  return methods;
}

export function finishFrostBindingCallRecording(): string[] {
  assert(activeFrostBindingRecorder, "FROST binding recording was not started");

  const recorder = activeFrostBindingRecorder;
  activeFrostBindingRecorder = undefined;
  recorder.restore();
  return [...recorder.calls].sort();
}

function getLocalWalletOptions(): ConfigOptions {
  configureBrowserBitcoinRpcProxy(window.location.origin);
  const env = (
    import.meta as ImportMeta & {
      readonly env: Record<string, string | undefined>;
    }
  ).env;
  return {
    ...getExampleWalletOptions(env, "LOCAL", window.location.origin),
    log: false,
    optimizationOptions: { auto: false },
    tokenTransactionVersion: "V3",
    useTokenPrimitivesBindings: true,
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function bitcoinRpc<T>(method: string, params: unknown[]): Promise<T> {
  const response = await fetch("/bitcoin-rpc", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      jsonrpc: "1.0",
      id: "spark-browser-integration",
      method,
      params,
    }),
  });
  assert(response.ok, `Bitcoin RPC ${method} returned HTTP ${response.status}`);

  const payload = (await response.json()) as {
    result: T;
    error: { message: string } | null;
  };
  assert(
    !payload.error,
    `Bitcoin RPC ${method} failed: ${payload.error?.message}`,
  );
  return payload.result;
}

async function claimDepositWithRetry(
  wallet: SparkWallet,
  txid: string,
): Promise<void> {
  let lastError: unknown;
  for (let attempt = 0; attempt < 20; attempt += 1) {
    try {
      await wallet.claimDeposit(txid);
      return;
    } catch (error) {
      lastError = error;
      await sleep(2_000);
    }
  }
  throw lastError;
}

async function waitForSatsBalance(
  wallet: SparkWallet,
  minimum: bigint,
): Promise<bigint> {
  const deadline = Date.now() + 60_000;
  let balance = 0n;
  while (Date.now() < deadline) {
    await wallet.experimental_syncWallet();
    balance = (await wallet.getBalance()).balance;
    if (balance >= minimum) {
      return balance;
    }
    await sleep(2_000);
  }
  throw new Error(
    `Timed out waiting for ${minimum} sats; last balance ${balance}`,
  );
}

async function checkFrostSigning(): Promise<void> {
  const frost = getSparkFrost();
  const signature = await frost.signFrost({
    message: hexToBytes(
      "309cfa947e5132b6a8dfec4f7c5a2b118f6a6339b29bb5f9eda67772cc4ca2ab",
    ),
    keyPackage: {
      secretKey: hexToBytes(
        "ea55351bebc990fb9e2c20f6e4172334c37c2ee644bd77830b2181da7dc4d991",
      ),
      publicKey: hexToBytes(
        "035e8f0057ae1d51ff2e88586bd5bf2e16bd87a3c4aac68945cbc96017e080e26e",
      ),
      verifyingKey: hexToBytes(
        "0265c8a4fd5613f89fbd88713ae6707d5bde332a35fc69bae105a10bbb93431480",
      ),
    },
    nonce: {
      hiding: hexToBytes(
        "977e1e21064e00f0ca089e923fc303375fe370f7b62c954cd76cde5a6ef0dc9c",
      ),
      binding: hexToBytes(
        "1b1cce7906d81a968deebf5ba8c8e2e72d05bfebfdab899326fd5d97dd4e8aef",
      ),
    },
    selfCommitment: {
      hiding: hexToBytes(
        "02e15ecb9c56f12ba55f47264f3dd21748b578dc5ffd26201547103e02fb281864",
      ),
      binding: hexToBytes(
        "035e38cdc33fd24dee735dc398dafd4d8c9e44da6aa45576b184786b517b8d61f1",
      ),
    },
    statechainCommitments: {
      "0000000000000000000000000000000000000000000000000000000000000003": {
        hiding: hexToBytes(
          "021cf1b3646f95cc6b2f8fd60290733b97bcafab8f0c513289c319bada58c5e01e",
        ),
        binding: hexToBytes(
          "03e9ba1827a469d925cc286f18a7cd1122bcd866f6263f8c49f0441f9d61226e32",
        ),
      },
      "0000000000000000000000000000000000000000000000000000000000000002": {
        hiding: hexToBytes(
          "024acf3d72ce07efaf55f2229895faa936a9c8aa635198953096b7c30ad69492ea",
        ),
        binding: hexToBytes(
          "0259f706606ecf5ef4fa02f5109c1e498c75b4c679d3410e6248a343bdf6419921",
        ),
      },
    },
    adaptorPubKey: undefined,
  });

  assert(
    bytesToHex(signature) ===
      "2ee25c78d61fc3ae8e4c91059369f23fd7a04ea54a43afe1f681276a063659e2",
    "signFrost did not match the cross-runtime fixture",
  );
}

async function checkFrostAggregation(): Promise<void> {
  const frost = getSparkFrost();
  const signature = await frost.aggregateFrost({
    message: hexToBytes(
      "05454bd3d25b76a39d068adb14c37b33ffe8160816c26092626c828f87c0ffd0",
    ),
    selfCommitment: {
      hiding: hexToBytes(
        "0320e8527b032ea3dd63d23c8d4fd67fc5aa2105886f771b9cefb8c438402fa1c0",
      ),
      binding: hexToBytes(
        "030ee0590f12b0d8250f5c3663ea8302d2b545f96019ac31279c2e2677d8cbcacc",
      ),
    },
    selfPublicKey: hexToBytes(
      "037433433c48a1a35688b687b0eb39c772e7f1b4e368feae4b5a33f075e46bb5f7",
    ),
    selfSignature: hexToBytes(
      "3f052b119fb2174d8c89761958c06506da91924b68e041c76f574aeb19e01b91",
    ),
    verifyingKey: hexToBytes(
      "02e5db919064ddb4807aca0898b2251e139ec18a9faff07e54125438a0faefc761",
    ),
    statechainCommitments: {
      "0000000000000000000000000000000000000000000000000000000000000003": {
        hiding: hexToBytes(
          "03669678988f4e002412d0c8c37eb8fd4f2a30b8cefbd26f6b54163c4402dad300",
        ),
        binding: hexToBytes(
          "03e079bf59fc1026d1c04cb77c95e0313487bced814996357aefa977573e30412c",
        ),
      },
      "0000000000000000000000000000000000000000000000000000000000000002": {
        hiding: hexToBytes(
          "035a0be8e0d551197e81d69229f07d1636c50fe3118610f61951c961353b568e2e",
        ),
        binding: hexToBytes(
          "03c9754ab6396358693a987c72e83b3d8b410c96e07e1b2e5a727be83eb3e7af79",
        ),
      },
    },
    statechainSignatures: {
      "0000000000000000000000000000000000000000000000000000000000000003":
        hexToBytes(
          "ebcd40228211b67fb675e52fe6b2f222a122a59672c049482d46a3d415e5a88a",
        ),
      "0000000000000000000000000000000000000000000000000000000000000002":
        hexToBytes(
          "934c283988e240f08b0484a30a48e464f2b1012375a7f143fb2664608b24413b",
        ),
    },
    statechainPublicKeys: {
      "0000000000000000000000000000000000000000000000000000000000000003":
        hexToBytes(
          "03d09d62c1db20c8cb073a233d92d00e8eeec8e6b0e01004d0e3ee5ecfa58d4a0c",
        ),
      "0000000000000000000000000000000000000000000000000000000000000002":
        hexToBytes(
          "025c9e7d0c3f2507903935850ca679a9ad213db6228593001a8f857f5f91fea4a4",
        ),
    },
  });

  assert(
    bytesToHex(signature) ===
      "a32847dcb81a35679512dcdfb9398d1786c18d08166e29e7f8247a0fb1a69711be1e936daaa60ebdce03dfec49bc3b8fb3b65c1ea1ffdc17d7f1f492eab3c415",
    "aggregateFrost did not match the cross-runtime fixture",
  );
}

async function checkFrostUtilities(): Promise<void> {
  const frost = getSparkFrost();
  const privateKey = new Uint8Array(32).fill(1);
  const publicKey = frost.getPublicKeyBytes(privateKey);
  const publicKeys = frost.batchGetPublicKeyBytes([
    privateKey,
    new Uint8Array(32).fill(2),
  ]);
  assert(
    equalBytes(publicKeys[0]!, publicKey) && publicKeys.length === 2,
    "Public-key derivation failed",
  );

  const plaintext = new Uint8Array([10, 11, 12, 13]);
  const ciphertext = await frost.encryptEcies(plaintext, publicKey);
  const decrypted = await frost.decryptEcies(ciphertext, privateKey);
  assert(equalBytes(decrypted, plaintext), "ECIES round trip failed");

  const secret = hexToBytes(
    "7c4e7ac16fe48e26d685ef7c33f49c217decf339319db6ff20b13fc2e33cdabc",
  );
  const shares = await frost.splitSecretWithProofs(secret, 3, 5);
  assert(
    shares.length === 5,
    "Secret splitting returned the wrong share count",
  );
  await Promise.all(
    shares.map((share) =>
      frost.validateShare(
        share.share,
        share.index,
        share.threshold,
        share.proofs,
      ),
    ),
  );
  const recovered = await frost.recoverSecret(shares.slice(0, 3));
  assert(equalBytes(recovered, secret), "Secret recovery failed");

  const signer = new DefaultSparkSigner();
  await signer.createSparkWalletFromSeed(new Uint8Array(32).fill(7), 0);
  const hash = new Uint8Array(
    await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(
        "browser adaptor signature",
      ) as Uint8Array<ArrayBuffer>,
    ),
  );
  const originalSignature = await signer.signSchnorrWithIdentityKey(hash);
  const signerPublicKey = await signer.getIdentityPublicKey();
  const { adaptorSignature, adaptorPrivateKey } =
    frost.generateAdaptorFromSignature(originalSignature);
  const adaptorPublicKey = frost.getPublicKeyBytes(adaptorPrivateKey);
  assert(
    frost.validateAdaptorSignature(
      signerPublicKey,
      hash,
      adaptorSignature,
      adaptorPublicKey,
    ),
    "Generated adaptor signature did not validate",
  );
  const appliedSignature = frost.applyAdaptorToSignature(
    signerPublicKey,
    hash,
    adaptorSignature,
    adaptorPrivateKey,
  );
  assert(
    equalBytes(appliedSignature, originalSignature),
    "Applying an adaptor did not recover the original signature",
  );

  const existingAdaptorSignature = frost.generateSignatureFromExistingAdaptor(
    originalSignature,
    adaptorPrivateKey,
  );
  assert(
    frost.validateAdaptorSignature(
      signerPublicKey,
      hash,
      existingAdaptorSignature,
      adaptorPublicKey,
    ),
    "Existing adaptor signature did not validate",
  );
}

async function checkFrostTransactions(): Promise<void> {
  const frost = getSparkFrost();
  const receivingPublicKey = frost.getPublicKeyBytes(
    new Uint8Array(32).fill(3),
  );
  const address = getP2TRAddressFromPublicKey(
    receivingPublicKey,
    Network.REGTEST,
  );
  const dummyTx = await frost.createDummyTx(address, 65_536n);
  assert(
    dummyTx.tx.length > 0 && dummyTx.txid.length === 64,
    "Dummy tx failed",
  );

  const parentTx = getTxFromRawTxHex(PARENT_TX_HEX);
  const parentOutput = parentTx.getOutput(0);
  assert(
    parentOutput.script && parentOutput.amount,
    "Parent output is missing",
  );
  const pair = await frost.constructNodeTxPair(
    hexToBytes(PARENT_TX_HEX),
    0,
    address,
    1_000,
    1_050,
    955n,
  );
  assert(
    pair.cpfp.tx.length > 0 && pair.direct.tx.length > 0,
    "Node tx failed",
  );

  const sighash = await frost.computeMultiInputSighash(
    pair.cpfp.tx,
    0,
    [parentOutput.script],
    [Number(parentOutput.amount)],
  );
  assert(sighash.length === 32, "Multi-input sighash failed");

  const refunds = await frost.constructRefundTxTrio(
    pair.cpfp.tx,
    pair.direct.tx,
    0,
    receivingPublicKey,
    "regtest",
    900,
    950,
    955n,
  );
  assert(
    refunds.cpfp_refund.tx.length > 0 &&
      refunds.direct_refund?.tx.length &&
      refunds.direct_from_cpfp_refund.tx.length > 0,
    "Refund tx construction failed",
  );
}

export async function runFrostBindingChecks(): Promise<{
  availableMethods: string[];
  calledMethods: string[];
}> {
  const frost = getSparkFrost();
  const availableMethods = getFrostBindingMethods(frost);
  const recorder = recordMethodCalls(frost, availableMethods);

  try {
    await checkFrostSigning();
    await checkFrostAggregation();
    await checkFrostUtilities();
    await checkFrostTransactions();
  } finally {
    recorder.restore();
  }

  return {
    availableMethods,
    calledMethods: [...recorder.calls].sort(),
  };
}

export async function runTokenBindingChecks(): Promise<{
  manifestHash: string;
}> {
  const manifest = TransferManifest.fromJSON({
    version: 1,
    transferId: "0197f9a0-1111-7000-8000-000000000001",
    network: "REGTEST",
    edges: [
      {
        senderIdentityPublicKey: "AqGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGhoaGh",
        receiverIdentityPublicKey:
          "AlVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVVV",
        amount: { sats: "1000" },
      },
    ],
  });
  const signer = new DefaultSparkSigner();
  await signer.createSparkWalletFromSeed(new Uint8Array(32).fill(11), 0);

  let manifestHash: Uint8Array | undefined;
  const signature = await signTransferManifest(manifest, {
    signMessageWithIdentityKey: async (message) => {
      manifestHash = message.slice();
      return signer.signMessageWithIdentityKey(message);
    },
  });
  assert(manifestHash, "Manifest signer did not receive a hash");
  assert(
    bytesToHex(manifestHash) ===
      "56a23f58799776190d566f70d88d8c3be8db059a598c8932b3250a8076abbacd",
    "Token manifest hash did not match the cross-language fixture",
  );
  assert(
    await verifyTransferManifestSignature(
      manifest,
      signature,
      await signer.getIdentityPublicKey(),
    ),
    "Token manifest signature did not verify",
  );

  return { manifestHash: bytesToHex(manifestHash) };
}

export async function runWalletJourney(): Promise<{
  restoredAddress: string;
  transferId: string;
  receiverBalance: string;
}> {
  const options = getLocalWalletOptions();
  let initialWallet: SparkWallet | undefined;
  let sender: SparkWallet | undefined;
  let receiver: SparkWallet | undefined;

  try {
    const generated = await SparkWallet.initialize({ options });
    initialWallet = generated.wallet;
    assert(generated.mnemonic, "Generated wallet did not return a mnemonic");
    const originalAddress = await initialWallet.getSparkAddress();
    await initialWallet.cleanup();
    initialWallet = undefined;

    sender = (
      await SparkWallet.initialize({
        mnemonicOrSeed: generated.mnemonic,
        options,
      })
    ).wallet;
    const restoredAddress = await sender.getSparkAddress();
    assert(
      restoredAddress === originalAddress,
      "Mnemonic recovery changed address",
    );

    receiver = (await SparkWallet.initialize({ options })).wallet;
    const message = "Spark browser integration";
    const signature = await sender.signMessageWithIdentityKey(message);
    const compactSignature = await sender.signMessageWithIdentityKey(
      message,
      true,
    );
    assert(
      await sender.validateMessageWithIdentityKey(message, signature),
      "Identity signature did not validate",
    );
    assert(
      !(await sender.validateMessageWithIdentityKey(
        `${message} modified`,
        signature,
      )),
      "Identity signature validated a different message",
    );
    assert(
      !(await receiver.validateMessageWithIdentityKey(message, signature)),
      "Identity signature validated with a different wallet",
    );
    assert(
      compactSignature !== signature &&
        (await sender.validateMessageWithIdentityKey(
          message,
          compactSignature,
        )),
      "Compact identity signature failed",
    );

    const fundingAmount = 50_000;
    const depositAddress = await sender.getSingleUseDepositAddress();
    const txid = await bitcoinRpc<string>("sendtoaddress", [
      depositAddress,
      Number((fundingAmount / 100_000_000).toFixed(8)),
    ]);
    const miningAddress = await bitcoinRpc<string>("getnewaddress", []);
    await bitcoinRpc("generatetoaddress", [3, miningAddress]);
    await claimDepositWithRetry(sender, txid);
    await waitForSatsBalance(sender, BigInt(fundingAmount));

    const transferAmount = fundingAmount;
    const transfer = await sender.transfer({
      receiverSparkAddress: await receiver.getSparkAddress(),
      amountSats: transferAmount,
    });
    const receiverBalance = await waitForSatsBalance(
      receiver,
      BigInt(transferAmount),
    );

    return {
      restoredAddress,
      transferId: transfer.id,
      receiverBalance: receiverBalance.toString(),
    };
  } finally {
    await Promise.allSettled(
      [initialWallet, sender, receiver]
        .filter((wallet): wallet is SparkWallet => wallet !== undefined)
        .map((wallet) => wallet.cleanup()),
    );
  }
}

export async function runTokenLifecycle(): Promise<{
  tokenIdentifier: string;
  transactionId: string;
  receiverBalance: string;
}> {
  const options = getLocalWalletOptions();
  let issuer: IssuerSparkWallet | undefined;
  let receiver: SparkWallet | undefined;

  try {
    issuer = (await IssuerSparkWallet.initialize({ options })).wallet;
    receiver = (await SparkWallet.initialize({ options })).wallet;

    const suffix = crypto
      .randomUUID()
      .replaceAll("-", "")
      .slice(0, 4)
      .toUpperCase();
    const creation = await issuer.createToken({
      tokenName: `Browser${suffix}`,
      tokenTicker: `B${suffix}`,
      decimals: 0,
      isFreezable: false,
      maxSupply: 1_000_000n,
      returnIdentifierForCreate: true,
    });
    const tokenIdentifier = creation.tokenIdentifier;
    await issuer.mintTokens({
      tokenAmount: 1_000n,
      tokenIdentifier,
    });

    const invoiceAmount = 250n;
    const invoice = await receiver.createTokensInvoice({
      amount: invoiceAmount,
      tokenIdentifier,
      memo: "Browser token integration",
      expiryTime: new Date(Date.now() + 24 * 60 * 60 * 1_000),
    });
    const result = await issuer.fulfillSparkInvoice([{ invoice }]);
    assert(result.invalidInvoices.length === 0, "Token invoice was invalid");
    assert(
      result.tokenTransactionErrors.length === 0,
      `Token transfer failed: ${result.tokenTransactionErrors[0]?.error.message}`,
    );
    const transactionId = result.tokenTransactionSuccess[0]?.txid;
    assert(transactionId, "Token invoice did not produce a transaction");

    const deadline = Date.now() + 60_000;
    let receiverBalance = 0n;
    while (Date.now() < deadline) {
      await receiver.experimental_syncWallet();
      const balance = await receiver.getBalance();
      receiverBalance = filterTokenBalanceForTokenIdentifier(
        balance.tokenBalances,
        tokenIdentifier,
      ).ownedBalance;
      if (receiverBalance >= invoiceAmount) {
        break;
      }
      await sleep(2_000);
    }
    assert(
      receiverBalance >= invoiceAmount,
      `Token receiver balance was ${receiverBalance}`,
    );

    return {
      tokenIdentifier,
      transactionId,
      receiverBalance: receiverBalance.toString(),
    };
  } finally {
    await Promise.allSettled(
      [issuer, receiver]
        .filter(
          (wallet): wallet is IssuerSparkWallet | SparkWallet =>
            wallet !== undefined,
        )
        .map((wallet) => wallet.cleanup()),
    );
  }
}
