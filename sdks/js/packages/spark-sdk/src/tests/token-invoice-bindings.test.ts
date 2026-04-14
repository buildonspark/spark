import { numberToVarBytesBE } from "@noble/curves/utils";
import { describe, expect, it, jest, beforeEach } from "@jest/globals";
import { Network as NetworkProto } from "../proto/spark.js";
import { WalletConfigService } from "../services/config.js";
import type { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.node.js";
import {
  setSparkTokenPrimitivesOnce,
  SparkTokenPrimitivesBase,
} from "../token-primitives-bindings/token-primitives-bindings.js";
import type {
  BroadcastBuildRequestBindingParams,
  FinalizeTokenInvoiceRequestBindingParams,
  PartialTransferBuildResultBinding,
  PreparedTokenInvoiceBinding,
  PrepareTokenInvoiceRequestBindingParams,
  TransferBuildRequestBindingParams,
} from "../token-primitives-bindings/types.js";
import { decodeSparkAddress, encodeSparkAddress } from "../utils/address.js";
import { encodeBech32mTokenIdentifier } from "../utils/token-identifier.js";

let nextConnectionManager!: ConnectionManagerNodeJS;

const prepareTokenInvoiceMock =
  jest.fn<
    (
      request: PrepareTokenInvoiceRequestBindingParams,
    ) => Promise<PreparedTokenInvoiceBinding>
  >();
const finalizeTokenInvoiceMock =
  jest.fn<
    (request: FinalizeTokenInvoiceRequestBindingParams) => Promise<string>
  >();

class MockSparkTokenPrimitives extends SparkTokenPrimitivesBase {
  async constructPartialTransferTransaction(
    _request: TransferBuildRequestBindingParams,
  ): Promise<PartialTransferBuildResultBinding> {
    throw new Error("not used in this test");
  }

  async hashPartialTokenTransaction(
    _partialTokenTransactionBytes: Uint8Array,
  ): Promise<Uint8Array> {
    throw new Error("not used in this test");
  }

  async buildBroadcastTransactionRequest(
    _request: BroadcastBuildRequestBindingParams,
  ): Promise<Uint8Array> {
    throw new Error("not used in this test");
  }

  async prepareTokenInvoice(
    request: PrepareTokenInvoiceRequestBindingParams,
  ): Promise<PreparedTokenInvoiceBinding> {
    return await prepareTokenInvoiceMock(request);
  }

  async finalizeTokenInvoice(
    request: FinalizeTokenInvoiceRequestBindingParams,
  ): Promise<string> {
    return await finalizeTokenInvoiceMock(request);
  }
}

setSparkTokenPrimitivesOnce(new MockSparkTokenPrimitives());

class TestSparkWallet extends SparkWallet {
  protected override buildConnectionManager(
    _config: WalletConfigService,
  ): ConnectionManagerNodeJS {
    return nextConnectionManager;
  }

  public constructor(useTokenPrimitivesBindings: boolean = true) {
    super({
      network: "LOCAL",
      useTokenPrimitivesBindings,
    });
  }

  public async initializeSignerForTest(): Promise<void> {
    await this.config.signer.createSparkWalletFromSeed(
      new Uint8Array(32).fill(1),
      0,
    );
  }

  public getSigner() {
    return this.config.signer;
  }
}

const TEST_SENDER_IDENTITY_PUBKEY =
  "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";

describe("token invoice bindings", () => {
  beforeEach(() => {
    nextConnectionManager = {} as ConnectionManagerNodeJS;
    prepareTokenInvoiceMock.mockReset();
    finalizeTokenInvoiceMock.mockReset();
  });

  it("routes token invoice creation through token-primitives bindings", async () => {
    const preparedInvoice = {
      sparkInvoiceFieldsBytes: new Uint8Array([1, 2, 3]),
      sparkInvoiceHash: new Uint8Array([4, 5, 6]),
      unsignedSparkAddress: "sprt1unsignedinvoice",
    } satisfies PreparedTokenInvoiceBinding;
    const signedInvoice = encodeSparkAddress({
      identityPublicKey: TEST_SENDER_IDENTITY_PUBKEY,
      network: "LOCAL",
    });
    prepareTokenInvoiceMock.mockResolvedValue(preparedInvoice);
    finalizeTokenInvoiceMock.mockResolvedValue(signedInvoice);

    const wallet = new TestSparkWallet();
    await wallet.initializeSignerForTest();

    const signer = wallet.getSigner();
    const identityPublicKey = await signer.getIdentityPublicKey();
    const signature = new Uint8Array(64).fill(9);
    jest
      .spyOn(signer, "signSchnorrWithIdentityKey")
      .mockResolvedValue(signature);

    const rawTokenIdentifier = new Uint8Array(32).fill(7);
    const tokenIdentifier = encodeBech32mTokenIdentifier({
      tokenIdentifier: rawTokenIdentifier,
      network: "LOCAL",
    });
    const senderSparkAddress = encodeSparkAddress({
      identityPublicKey: TEST_SENDER_IDENTITY_PUBKEY,
      network: "LOCAL",
    });
    const expiryTime = new Date("2026-01-02T03:04:05.000Z");

    const result = await wallet.createTokensInvoice({
      tokenIdentifier,
      amount: 123n,
      memo: "token invoice memo",
      senderSparkAddress,
      expiryTime,
    });

    expect(result).toBe(signedInvoice);
    expect(prepareTokenInvoiceMock).toHaveBeenCalledWith({
      receiverIdentityPublicKey: identityPublicKey,
      network: NetworkProto.REGTEST,
      tokenIdentifier: rawTokenIdentifier,
      tokenAmount: numberToVarBytesBE(123n),
      memo: "token invoice memo",
      senderSparkAddress,
      expiryTimeUnixMillis: expiryTime.getTime(),
    });
    expect(signer.signSchnorrWithIdentityKey).toHaveBeenCalledWith(
      preparedInvoice.sparkInvoiceHash,
    );
    expect(finalizeTokenInvoiceMock).toHaveBeenCalledWith({
      receiverIdentityPublicKey: identityPublicKey,
      network: NetworkProto.REGTEST,
      sparkInvoiceFieldsBytes: preparedInvoice.sparkInvoiceFieldsBytes,
      signature,
    });
  });

  it("does not use token-primitives bindings unless enabled in config", async () => {
    const wallet = new TestSparkWallet(false);
    await wallet.initializeSignerForTest();

    const result = await wallet.createTokensInvoice({
      memo: "fallback token invoice",
    });

    expect(prepareTokenInvoiceMock).not.toHaveBeenCalled();
    expect(finalizeTokenInvoiceMock).not.toHaveBeenCalled();
    expect(
      decodeSparkAddress(result, "LOCAL").sparkInvoiceFields,
    ).toMatchObject({
      memo: "fallback token invoice",
      paymentType: {
        type: "tokens",
      },
    });
  });

  it("passes partial token invoice fields through to the bindings", async () => {
    prepareTokenInvoiceMock.mockResolvedValue({
      sparkInvoiceFieldsBytes: new Uint8Array([10]),
      sparkInvoiceHash: new Uint8Array([11]),
      unsignedSparkAddress: "sprt1unsignedpartialinvoice",
    });
    finalizeTokenInvoiceMock.mockResolvedValue(
      encodeSparkAddress({
        identityPublicKey: TEST_SENDER_IDENTITY_PUBKEY,
        network: "LOCAL",
      }),
    );

    const wallet = new TestSparkWallet();
    await wallet.initializeSignerForTest();

    const signer = wallet.getSigner();
    const identityPublicKey = await signer.getIdentityPublicKey();
    jest
      .spyOn(signer, "signSchnorrWithIdentityKey")
      .mockResolvedValue(new Uint8Array(64).fill(8));

    await wallet.createTokensInvoice({
      memo: "partial token invoice",
    });

    expect(prepareTokenInvoiceMock).toHaveBeenCalledWith({
      receiverIdentityPublicKey: identityPublicKey,
      network: NetworkProto.REGTEST,
      tokenIdentifier: undefined,
      tokenAmount: undefined,
      memo: "partial token invoice",
      senderSparkAddress: undefined,
      expiryTimeUnixMillis: undefined,
    });
  });
});
