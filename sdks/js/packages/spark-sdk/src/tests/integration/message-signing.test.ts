import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";

describe("Message signing", () => {
  let wallet1: SparkWalletTesting;
  let wallet2: SparkWalletTesting;

  beforeAll(async () => {
    const { wallet: newWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const { wallet: newWallet2 } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    wallet1 = newWallet;
    wallet2 = newWallet2;
  });

  afterAll(async () => {
    await wallet1?.cleanupConnections();
    await wallet2?.cleanupConnections();
  });

  it("should sign and validate messages", async () => {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message);
    const isValid = await wallet1.validateMessageWithIdentityKey(
      message,
      signature,
    );
    expect(isValid).toBe(true);
  });

  it("should sign and validate messages with compact encoding", async () => {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message, true);
    const isValid = await wallet1.validateMessageWithIdentityKey(
      message,
      signature,
    );
    expect(isValid).toBe(true);
  });

  it("compact encoding should be different from non-compact encoding", async () => {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message, true);
    const signature2 = await wallet1.signMessageWithIdentityKey(message);
    expect(signature).not.toBe(signature2);
  });

  it("should not validate messages signed by different keys", async () => {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message);
    const isValid = await wallet2.validateMessageWithIdentityKey(
      message,
      signature,
    );
    expect(isValid).toBe(false);
  });

  it("should not validate messages signed by different keys with compact encoding", async () => {
    const message = "Hello, world!";
    const signature = await wallet1.signMessageWithIdentityKey(message, true);
    const isValid = await wallet2.validateMessageWithIdentityKey(
      message,
      signature,
    );
    expect(isValid).toBe(false);
  });

  it("should not validate if message is different", async () => {
    const message = "Hello, world!";
    const message2 = "Hello, world!2";
    const signature = await wallet1.signMessageWithIdentityKey(message);
    const isValid = await wallet1.validateMessageWithIdentityKey(
      message2,
      signature,
    );
    expect(isValid).toBe(false);
  });
});
