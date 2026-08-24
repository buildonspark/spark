import { describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex } from "@noble/hashes/utils";
import type LightningReceiveRequest from "../graphql/objects/LightningReceiveRequest.js";
import { LightningService } from "../services/lightning.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import { setSparkFrostOnce } from "../spark-bindings/spark-bindings.js";
import { SparkFrost } from "../spark-bindings/spark-bindings.node.js";

setSparkFrostOnce(new SparkFrost());

const ATTESTOR = secp256k1.getPublicKey(new Uint8Array(32).fill(0x11), true);
const PAYEE = secp256k1.getPublicKey(new Uint8Array(32).fill(0x22), true);
const OPERATOR = secp256k1.getPublicKey(new Uint8Array(32).fill(0x33), true);

type StoredShare = { userIdentityPublicKey: Uint8Array };

function serviceWithCapture() {
  const stored: StoredShare[] = [];
  const signingOperators = {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identityPublicKey: bytesToHex(OPERATOR),
    },
  };

  const config = {
    getSigningOperators: () => signingOperators,
    getThreshold: () => 1,
    getCoordinatorAddress: () => "coordinator",
    signer: {
      getIdentityPublicKey: () => Promise.resolve(ATTESTOR),
      splitSecretWithProofs: () =>
        Promise.resolve([{ share: new Uint8Array(32).fill(0x44), proofs: [] }]),
    },
  };

  const connectionManager = {
    createSparkClient: () =>
      Promise.resolve({
        store_preimage_share_v2: (req: StoredShare) => {
          stored.push(req);
          return Promise.resolve({});
        },
      }),
  };

  // Intersecting with LightningService collapses to never — config is private on
  // the class — so name only the surface this path touches.
  const service = Object.create(LightningService.prototype) as {
    config: typeof config;
    connectionManager: typeof connectionManager;
    createLightningInvoiceWithPreImage: LightningService["createLightningInvoiceWithPreImage"];
    createLightningInvoice: LightningService["createLightningInvoice"];
  };
  service.config = config;
  service.connectionManager = connectionManager;

  return { service, stored };
}

const invoiceCreator = jest.fn(() =>
  Promise.resolve({
    invoice: { encodedInvoice: "lnbcrt1" },
  } as unknown as LightningReceiveRequest),
);

async function ownerOf(
  params: {
    receiverIdentityPubkey?: string;
    retainPreimageShareOwnership?: boolean;
  },
  entry: "withPreImage" | "createInvoice" = "withPreImage",
): Promise<Uint8Array> {
  const { service, stored } = serviceWithCapture();

  if (entry === "createInvoice") {
    await service.createLightningInvoice({
      invoiceCreator,
      amountSats: 1000,
      ...params,
    });
  } else {
    await service.createLightningInvoiceWithPreImage({
      invoiceCreator,
      amountSats: 1000,
      preimage: new Uint8Array(32).fill(0x55),
      ...params,
    });
  }

  expect(stored).toHaveLength(1);
  return stored[0]!.userIdentityPublicKey;
}

// The operators resolve the attestor as the preimage share's owner, so this
// selection decides whether a delegated receive can be verified at all.
describe("preimage share ownership", () => {
  it("leaves the share with the payee on an unquoted receive", async () => {
    expect(
      await ownerOf({ receiverIdentityPubkey: bytesToHex(PAYEE) }),
    ).toEqual(PAYEE);
  });

  it("keeps the share with the attestor on a quoted delegated receive", async () => {
    expect(
      await ownerOf({
        receiverIdentityPubkey: bytesToHex(PAYEE),
        retainPreimageShareOwnership: true,
      }),
    ).toEqual(ATTESTOR);
  });

  it("keeps the share with this wallet when no payee is named", async () => {
    expect(await ownerOf({ retainPreimageShareOwnership: true })).toEqual(
      ATTESTOR,
    );
  });

  it("retains only when a quote is present, at the wallet boundary", async () => {
    const seen: (boolean | undefined)[] = [];
    const wallet = Object.create(SparkWallet.prototype) as {
      lightningService: {
        createLightningInvoice: (
          p: Record<string, unknown>,
        ) => Promise<unknown>;
      };
      validateAndCreateLightningInvoice: () => Promise<unknown>;
      createLightningInvoice: SparkWallet["createLightningInvoice"];
    };
    wallet.lightningService = {
      createLightningInvoice: (p) => {
        seen.push(p["retainPreimageShareOwnership"] as boolean | undefined);
        return Promise.resolve({});
      },
    };
    wallet.validateAndCreateLightningInvoice = () => Promise.resolve({});

    await wallet.createLightningInvoice({ amountSats: 1000 });
    await wallet.createLightningInvoice({
      amountSats: 1000,
      quote: {} as never,
    });

    expect(seen).toEqual([false, true]);
  });

  it("carries the flag through the public entry point", async () => {
    expect(
      await ownerOf(
        {
          receiverIdentityPubkey: bytesToHex(PAYEE),
          retainPreimageShareOwnership: true,
        },
        "createInvoice",
      ),
    ).toEqual(ATTESTOR);
  });

  it("carries its absence through too", async () => {
    expect(
      await ownerOf(
        { receiverIdentityPubkey: bytesToHex(PAYEE) },
        "createInvoice",
      ),
    ).toEqual(PAYEE);
  });
});
