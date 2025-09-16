import { walletTypes, createDeterministicKeys } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";
import { TokenTransactionService } from "../../services/token-transactions.js";
import { ValidationError } from "../../errors/types.js";

describe.each(walletTypes)(
  "fetch owned token outputs should fail with invalid inputs",
  ({ Signer }) => {
    let tokenTransactionService: TokenTransactionService;

    beforeEach(async () => {
      const userWallet = await SparkWalletTesting.initialize({
        options: {
          network: "LOCAL",
        },
        signer: new Signer(),
      });
      const wallet = userWallet.wallet;

      const userConfig = new WalletConfigService(
        {
          network: "LOCAL",
        },
        wallet.getSigner(),
      );
      const connectionManager = new ConnectionManagerNodeJS(userConfig);
      tokenTransactionService = new TokenTransactionService(
        userConfig,
        connectionManager,
      );
    });

    it("should fail with empty owner public keys", async () => {
      await expect(
        tokenTransactionService.fetchOwnedTokenOutputs({
          ownerPublicKeys: [],
        }),
      ).rejects.toThrow(ValidationError);
    });

    it("should fail with malformed owner public key", async () => {
      const malformedKey = new Uint8Array(32).fill(1);

      await expect(
        tokenTransactionService.fetchOwnedTokenOutputs({
          ownerPublicKeys: [malformedKey],
        }),
      ).rejects.toThrow(ValidationError);
    });

    it("should fail with malformed issuer public key", async () => {
      const { publicKey } = createDeterministicKeys(
        expect.getState().currentTestName!,
      );
      const malformedKey = new Uint8Array(32).fill(1);

      await expect(
        tokenTransactionService.fetchOwnedTokenOutputs({
          ownerPublicKeys: [publicKey],
          issuerPublicKeys: [malformedKey],
        }),
      ).rejects.toThrow(ValidationError);
    });

    it("should fail with malformed token identifier", async () => {
      const { publicKey } = createDeterministicKeys(
        expect.getState().currentTestName!,
      );
      const malformedIdentifier = new Uint8Array(99).fill(1);

      await expect(
        tokenTransactionService.fetchOwnedTokenOutputs({
          ownerPublicKeys: [publicKey],
          issuerPublicKeys: [publicKey],
          tokenIdentifiers: [malformedIdentifier],
        }),
      ).rejects.toThrow(ValidationError);
    });
  },
);
