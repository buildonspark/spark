import { describe, expect, it, jest } from "@jest/globals";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { Transaction } from "@scure/btc-signer";
import type {
  AggregateFrostParams,
  KeyDerivation,
  SignFrostParams,
  SigningCommitmentWithOptionalNonce,
} from "../signer/types.js";
import { KeyDerivationType } from "../signer/types.js";
import {
  BaseTransferService,
  type LeafRefundSigningData,
} from "../services/transfer.js";
import type {
  LeafRefundTxSigningResult,
  SigningResult as ProtoSigningResult,
} from "../proto/spark.js";
import { getSigHashFromTx, getTxFromRawTxHex } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { createInitialTimelockRefundTxs } from "../utils/transaction.js";

describe("transfer", () => {
  function makeCommitment(): SigningCommitmentWithOptionalNonce {
    return {
      commitment: {
        binding: new Uint8Array(33),
        hiding: new Uint8Array(33),
      },
    };
  }

  it("signRefunds signs direct-from-cpfp refund without direct tx", async () => {
    // Use a known-good tx from existing bitcoin tests (has non-zero output amount + script).
    const nodeTx = getTxFromRawTxHex(
      "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000",
    );

    const receivingPubkey = hexToBytes(
      "03ef261da8259f5ea86abe1b7d658ebd13fe2b2ce2418dabe854ccec67bdb9ba87",
    );

    const { cpfpRefundTx, directFromCpfpRefundTx } =
      createInitialTimelockRefundTxs({
        nodeTx,
        receivingPubkey,
        network: Network.MAINNET,
      });

    if (!directFromCpfpRefundTx) {
      throw new Error("Expected directFromCpfpRefundTx to be defined");
    }

    const commitment = makeCommitment();

    const leafId = "leaf-1";
    const leafData: LeafRefundSigningData = {
      keyDerivation: { type: KeyDerivationType.LEAF, path: "m/0" },
      receivingPubkey,
      signingNonceCommitment: commitment,
      directSigningNonceCommitment: commitment,
      directFromCpfpRefundSigningNonceCommitment: commitment,
      tx: nodeTx,
      // No directTx: simulate non-watchtower-ready leaf.
      refundTx: cpfpRefundTx,
      directFromCpfpRefundTx,
      vout: 0,
    };

    const leafDataMap = new Map<string, LeafRefundSigningData>([
      [leafId, leafData],
    ]);

    const dummySigningResult: ProtoSigningResult = {
      publicKeys: {},
      signingNonceCommitments: {},
      signatureShares: {},
      signingKeyshare: undefined,
    };

    const operatorSigningResults: LeafRefundTxSigningResult[] = [
      {
        leafId,
        verifyingKey: new Uint8Array(33),
        refundTxSigningResult: dummySigningResult,
        directRefundTxSigningResult: undefined,
        directFromCpfpRefundTxSigningResult: dummySigningResult,
      },
    ];

    let aggregateCalls = 0;
    const signer = {
      getPublicKeyFromDerivation: jest.fn(
        async (_keyDerivation?: KeyDerivation) => receivingPubkey,
      ),
      signFrost: jest.fn(
        async (_params: SignFrostParams) => new Uint8Array([9]),
      ),
      aggregateFrost: jest.fn(
        async (_params: AggregateFrostParams) =>
          new Uint8Array([++aggregateCalls]),
      ),
    };

    const service = { config: { signer } } as unknown as BaseTransferService;
    const signatures = await BaseTransferService.prototype.signRefunds.call(
      service,
      leafDataMap,
      operatorSigningResults,
    );

    expect(signer.aggregateFrost).toHaveBeenCalledTimes(2);
    expect(signatures).toHaveLength(1);
    expect(signatures[0]?.nodeId).toBe(leafId);
    expect(signatures[0]?.directRefundTxSignature).toHaveLength(0);
    expect(
      signatures[0]?.directFromCpfpRefundTxSignature.length,
    ).toBeGreaterThan(0);
  });

  it("signRefunds signs direct refund when direct tx exists and uses correct prevout for each sighash", async () => {
    const nodeTx = getTxFromRawTxHex(
      "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000",
    );
    const nodeTxOutput = nodeTx.getOutput(0);
    if (!nodeTxOutput?.script || nodeTxOutput.amount === undefined) {
      throw new Error("Missing node tx output script/amount");
    }
    if (nodeTxOutput.amount <= 2000n) {
      throw new Error("Node tx output amount too small for test");
    }

    // Create a direct tx with a DIFFERENT output amount than the node tx so we can
    // prove direct-from-cpfp uses the node tx prevout (not the direct tx prevout).
    const directTx = new Transaction({
      version: 3,
      allowUnknownOutputs: true,
    });
    directTx.addInput({
      txid: "00".repeat(32),
      index: 0,
    });
    directTx.addOutput({
      script: nodeTxOutput.script,
      amount: nodeTxOutput.amount - 1000n,
    });

    const receivingPubkey = hexToBytes(
      "03ef261da8259f5ea86abe1b7d658ebd13fe2b2ce2418dabe854ccec67bdb9ba87",
    );

    const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
      createInitialTimelockRefundTxs({
        nodeTx,
        directNodeTx: directTx,
        receivingPubkey,
        network: Network.MAINNET,
      });

    if (!directRefundTx || !directFromCpfpRefundTx) {
      throw new Error("Expected direct refund txs to be defined");
    }

    const cpfpCommitment = makeCommitment();
    const directCommitment = makeCommitment();
    const directFromCpfpCommitment = makeCommitment();

    const leafId = "leaf-1";
    const leafData: LeafRefundSigningData = {
      keyDerivation: { type: KeyDerivationType.LEAF, path: "m/0" },
      receivingPubkey,
      signingNonceCommitment: cpfpCommitment,
      directSigningNonceCommitment: directCommitment,
      directFromCpfpRefundSigningNonceCommitment: directFromCpfpCommitment,
      tx: nodeTx,
      directTx,
      refundTx: cpfpRefundTx,
      directRefundTx,
      directFromCpfpRefundTx,
      vout: 0,
    };

    const leafDataMap = new Map<string, LeafRefundSigningData>([
      [leafId, leafData],
    ]);

    const dummySigningResult: ProtoSigningResult = {
      publicKeys: {},
      signingNonceCommitments: {},
      signatureShares: {},
      signingKeyshare: undefined,
    };

    const operatorSigningResults: LeafRefundTxSigningResult[] = [
      {
        leafId,
        verifyingKey: new Uint8Array(33),
        refundTxSigningResult: dummySigningResult,
        directRefundTxSigningResult: dummySigningResult,
        directFromCpfpRefundTxSigningResult: dummySigningResult,
      },
    ];

    let aggregateCalls = 0;
    const signer = {
      getPublicKeyFromDerivation: jest.fn(
        async (_keyDerivation?: KeyDerivation) => receivingPubkey,
      ),
      signFrost: jest.fn(
        async (_params: SignFrostParams) => new Uint8Array([9]),
      ),
      aggregateFrost: jest.fn(
        async (_params: AggregateFrostParams) =>
          new Uint8Array([++aggregateCalls]),
      ),
    };

    const service = { config: { signer } } as unknown as BaseTransferService;
    const signatures = await BaseTransferService.prototype.signRefunds.call(
      service,
      leafDataMap,
      operatorSigningResults,
    );

    expect(signer.getPublicKeyFromDerivation).toHaveBeenCalledTimes(1);
    expect(signer.signFrost).toHaveBeenCalledTimes(3);
    expect(signer.aggregateFrost).toHaveBeenCalledTimes(3);

    // Verify we used the correct commitments per signature.
    const signCalls = signer.signFrost.mock.calls.map((c) => c[0]);
    const [cpfpCall, directCall, directFromCpfpCall] = signCalls;
    if (!cpfpCall || !directCall || !directFromCpfpCall) {
      throw new Error("Expected 3 signFrost calls");
    }
    expect(cpfpCall.selfCommitment).toBe(cpfpCommitment);
    expect(directCall.selfCommitment).toBe(directCommitment);
    expect(directFromCpfpCall.selfCommitment).toBe(directFromCpfpCommitment);

    // Verify we used the correct sighash for each signature:
    // - CPFP refund prevout is node tx output
    // - Direct refund prevout is direct tx output
    // - Direct-from-CPFP refund prevout is node tx output (NOT direct tx output)
    const directTxOutput = directTx.getOutput(0);
    if (!directTxOutput?.script || directTxOutput.amount === undefined) {
      throw new Error("Missing direct tx output script/amount");
    }

    const expectedCpfpSighash = getSigHashFromTx(cpfpRefundTx, 0, nodeTxOutput);
    const expectedDirectSighash = getSigHashFromTx(
      directRefundTx,
      0,
      directTxOutput,
    );
    const expectedDirectFromCpfpSighash = getSigHashFromTx(
      directFromCpfpRefundTx,
      0,
      nodeTxOutput,
    );
    const wrongDirectFromCpfpSighash = getSigHashFromTx(
      directFromCpfpRefundTx,
      0,
      directTxOutput,
    );

    expect(bytesToHex(cpfpCall.message)).toBe(bytesToHex(expectedCpfpSighash));
    expect(bytesToHex(directCall.message)).toBe(
      bytesToHex(expectedDirectSighash),
    );
    expect(bytesToHex(directFromCpfpCall.message)).toBe(
      bytesToHex(expectedDirectFromCpfpSighash),
    );
    expect(bytesToHex(directFromCpfpCall.message)).not.toBe(
      bytesToHex(wrongDirectFromCpfpSighash),
    );

    expect(signatures).toHaveLength(1);
    expect(signatures[0]?.nodeId).toBe(leafId);
    expect(signatures[0]?.directRefundTxSignature.length).toBeGreaterThan(0);
    expect(
      signatures[0]?.directFromCpfpRefundTxSignature.length,
    ).toBeGreaterThan(0);
  });

  it("signRefunds skips direct refund signing when directRefundTx is absent (e.g. zero-timelock leaf)", async () => {
    const nodeTx = getTxFromRawTxHex(
      "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000",
    );
    const nodeTxOutput = nodeTx.getOutput(0);
    if (!nodeTxOutput?.script || nodeTxOutput.amount === undefined) {
      throw new Error("Missing node tx output script/amount");
    }

    const directTx = new Transaction({
      version: 3,
      allowUnknownOutputs: true,
    });
    directTx.addInput({
      txid: "00".repeat(32),
      index: 0,
    });
    directTx.addOutput({
      script: nodeTxOutput.script,
      amount: nodeTxOutput.amount,
    });

    const receivingPubkey = hexToBytes(
      "03ef261da8259f5ea86abe1b7d658ebd13fe2b2ce2418dabe854ccec67bdb9ba87",
    );

    const { cpfpRefundTx, directFromCpfpRefundTx } =
      createInitialTimelockRefundTxs({
        nodeTx,
        directNodeTx: directTx,
        receivingPubkey,
        network: Network.MAINNET,
      });

    if (!directFromCpfpRefundTx) {
      throw new Error("Expected directFromCpfpRefundTx to be defined");
    }

    const cpfpCommitment = makeCommitment();
    const directCommitment = makeCommitment();
    const directFromCpfpCommitment = makeCommitment();

    const leafId = "leaf-1";
    const leafData: LeafRefundSigningData = {
      keyDerivation: { type: KeyDerivationType.LEAF, path: "m/0" },
      receivingPubkey,
      signingNonceCommitment: cpfpCommitment,
      directSigningNonceCommitment: directCommitment,
      directFromCpfpRefundSigningNonceCommitment: directFromCpfpCommitment,
      tx: nodeTx,
      directTx,
      refundTx: cpfpRefundTx,
      // directRefundTx intentionally omitted
      directFromCpfpRefundTx,
      vout: 0,
    };

    const leafDataMap = new Map<string, LeafRefundSigningData>([
      [leafId, leafData],
    ]);

    const dummySigningResult: ProtoSigningResult = {
      publicKeys: {},
      signingNonceCommitments: {},
      signatureShares: {},
      signingKeyshare: undefined,
    };

    const operatorSigningResults: LeafRefundTxSigningResult[] = [
      {
        leafId,
        verifyingKey: new Uint8Array(33),
        refundTxSigningResult: dummySigningResult,
        directRefundTxSigningResult: undefined,
        directFromCpfpRefundTxSigningResult: dummySigningResult,
      },
    ];

    const signer = {
      getPublicKeyFromDerivation: jest.fn(
        async (_keyDerivation?: KeyDerivation) => receivingPubkey,
      ),
      signFrost: jest.fn(
        async (_params: SignFrostParams) => new Uint8Array([9]),
      ),
      aggregateFrost: jest.fn(
        async (_params: AggregateFrostParams) => new Uint8Array([1]),
      ),
    };

    const service = { config: { signer } } as unknown as BaseTransferService;
    const signatures = await BaseTransferService.prototype.signRefunds.call(
      service,
      leafDataMap,
      operatorSigningResults,
    );

    // CPFP + direct-from-CPFP only.
    expect(signer.signFrost).toHaveBeenCalledTimes(2);
    expect(signer.aggregateFrost).toHaveBeenCalledTimes(2);

    const signCalls = signer.signFrost.mock.calls.map((c) => c[0]);
    const [cpfpCall, directFromCpfpCall] = signCalls;
    if (!cpfpCall || !directFromCpfpCall) {
      throw new Error("Expected 2 signFrost calls");
    }
    expect(cpfpCall.selfCommitment).toBe(cpfpCommitment);
    expect(directFromCpfpCall.selfCommitment).toBe(directFromCpfpCommitment);

    expect(signatures).toHaveLength(1);
    expect(signatures[0]?.nodeId).toBe(leafId);
    expect(signatures[0]?.directRefundTxSignature).toHaveLength(0);
    expect(
      signatures[0]?.directFromCpfpRefundTxSignature.length,
    ).toBeGreaterThan(0);
  });
});
