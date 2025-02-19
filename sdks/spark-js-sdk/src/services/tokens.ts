import { secp256k1 } from "@noble/curves/secp256k1";
import {
  OperatorSpecificTokenTransactionSignablePayload,
  OperatorSpecificTokenTransactionSignature,
  SignTokenTransactionResponse,
  TokenTransaction,
} from "../proto/spark";
import { WalletConfigService } from "../services/config";
import {
  hashOperatorSpecificTokenTransactionSignablePayload,
  hashTokenTransaction,
  recoverPrivateKeyFromKeyshares,
} from "../utils/tokens";
import { ConnectionManager } from "./connection";

export type TokenLeafCreationData = {
  tokenPublicKey: Uint8Array;
  /** uint128 */
  tokenAmount: Uint8Array;
  withdrawalBondSats: number;
  withdrawalLocktime: number;
};

export interface TokenTransferData {
  // The hash of the previous token transaction containing the leaves to spend
  prevTokenTransactionHash: Uint8Array;
  // The indices of leaves to spend from the previous transaction
  leavesToSpendIndices: number[];
  // Data for new output leaves to create
  outputLeafData: TokenLeafCreationData[];
}

export class TokenTransactionService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    // Not necessary if it's a mint transaction
    leafToSpendPrivateKeys?: Uint8Array[],
    leafToSpendRevocationPublicKeys?: Uint8Array[]
  ): Promise<TokenTransaction> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const signingOperatorResponse = await sparkClient.get_signing_operator_list(
      {}
    );
    const operatorKeys: Uint8Array[] = [];
    for (const [_, operator] of Object.entries(
      signingOperatorResponse.signingOperators
    )) {
      operatorKeys.push(operator.publicKey);
    }

    tokenTransaction.sparkOperatorIdentityPublicKeys = operatorKeys;

    const partialTokenTransactionHash = hashTokenTransaction(
      tokenTransaction,
      true
    );

    const ownerSignatures: Uint8Array[] = [];
    if (tokenTransaction.tokenInput!.$case === "mintInput") {
      // For issuance transaction, sign with identity private key
      const derSignatureBytes =
        await this.config.signer.signMessageWithIdentityKey(
          partialTokenTransactionHash
        );

      ownerSignatures.push(derSignatureBytes);
    } else if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;

      transferInput.leavesToSpend.forEach((leaf, index) => {
        const derSignatureBytes = secp256k1
          .sign(partialTokenTransactionHash, leafToSpendPrivateKeys![index])
          .toDERRawBytes();

        ownerSignatures.push(derSignatureBytes);
      });
    }

    // Start the token transaction
    const startResponse = await sparkClient.start_token_transaction({
      identityPublicKey: await this.config.signer.getIdentityPublicKey(),
      partialTokenTransaction: tokenTransaction,
      tokenTransactionSignatures: {
        ownerSignatures: ownerSignatures,
      },
    });

    // Validate keyshare configuration
    if (
      startResponse.keyshareInfo?.ownerIdentifiers.length !==
      Object.keys(signingOperatorResponse.signingOperators).length
    ) {
      throw new Error(
        `Keyshare operator count (${
          startResponse.keyshareInfo?.ownerIdentifiers.length
        }) does not match signing operator count (${
          Object.keys(signingOperatorResponse.signingOperators).length
        })`
      );
    }

    for (const identifier of startResponse.keyshareInfo?.ownerIdentifiers ||
      []) {
      if (!signingOperatorResponse.signingOperators[identifier]) {
        throw new Error(
          `Keyshare operator ${identifier} not found in signing operator list`
        );
      }
    }

    const finalTokenTransaction = startResponse.finalTokenTransaction!;
    const finalTokenTransactionHash = hashTokenTransaction(
      finalTokenTransaction,
      false
    );

    const payload: OperatorSpecificTokenTransactionSignablePayload = {
      finalTokenTransactionHash: finalTokenTransactionHash,
      operatorIdentityPublicKey:
        await this.config.signer.getIdentityPublicKey(),
    };

    const payloadHash =
      await hashOperatorSpecificTokenTransactionSignablePayload(payload);

    const operatorSpecificSignatures: OperatorSpecificTokenTransactionSignature[] =
      [];
    if (tokenTransaction.tokenInput!.$case === "mintInput") {
      const derSignatureBytes =
        await this.config.signer.signMessageWithIdentityKey(payloadHash);

      operatorSpecificSignatures.push({
        ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
        ownerSignature: derSignatureBytes,
        payload: payload,
      });
    }

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;
      for (const leaf of transferInput.leavesToSpend) {
        const derSignatureBytes =
          await this.config.signer.signMessageWithIdentityKey(payloadHash);

        operatorSpecificSignatures.push({
          ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
          ownerSignature: derSignatureBytes,
          payload: payload,
        });
      }
    }

    // Submit sign_token_transaction to all SOs in parallel and track their indices
    const soSignatures = await Promise.allSettled(
      Object.entries(signingOperatorResponse.signingOperators).map(
        async ([identifier, operator], index) => {
          const internalSparkClient =
            await this.connectionManager.createSparkClient(operator.address);
          const response = await internalSparkClient.sign_token_transaction({
            finalTokenTransaction,
            operatorSpecificSignatures,
          });

          return {
            index,
            identifier,
            response,
          };
        }
      )
    );

    const threshold = startResponse.keyshareInfo.threshold;

    // Collect successful signatures with their indices
    const successfulSignatures = soSignatures
      .filter(
        (
          result
        ): result is PromiseFulfilledResult<{
          index: number;
          identifier: string;
          response: SignTokenTransactionResponse;
        }> => result.status === "fulfilled"
      )
      .map((result) => result.value);

    if (successfulSignatures.length < threshold) {
      const errors = soSignatures
        .filter(
          (result): result is PromiseRejectedResult =>
            result.status === "rejected"
        )
        .map((result) => result.reason)
        .join("\n");

      throw new Error(
        `Failed to collect enough signatures. Got ${successfulSignatures.length}/${threshold} required.\nErrors:\n${errors}`
      );
    }

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const leavesToSpend =
        tokenTransaction.tokenInput!.transferInput.leavesToSpend;

      let revocationKeys: Uint8Array[] = [];

      leavesToSpend.forEach((leaf, leafIndex) => {
        // For each leaf, collect keyshares from all SOs that responded successfully
        const leafKeyshares = successfulSignatures.map(
          ({ identifier, response }) => ({
            index: parseInt(identifier, 16),
            keyshare: response.tokenTransactionRevocationKeyshares[leafIndex],
          })
        );

        if (leafKeyshares.length < threshold) {
          throw new Error(
            `Insufficient keyshares for leaf ${leafIndex}: got ${leafKeyshares.length}, need ${threshold}`
          );
        }

        // Check for duplicate operator indices
        const seenIndices = new Set<number>();
        for (const { index } of leafKeyshares) {
          if (seenIndices.has(index)) {
            throw new Error(
              `Duplicate operator index ${index} for leaf ${leafIndex}`
            );
          }
          seenIndices.add(index);
        }

        const recoveredPrivateKey = recoverPrivateKeyFromKeyshares(
          leafKeyshares,
          threshold
        );
        const recoveredPublicKey = secp256k1.getPublicKey(
          recoveredPrivateKey,
          true
        );

        if (
          !leafToSpendRevocationPublicKeys ||
          !leafToSpendRevocationPublicKeys[leafIndex] ||
          !recoveredPublicKey.every(
            (byte, i) => byte === leafToSpendRevocationPublicKeys[leafIndex][i]
          )
        ) {
          throw new Error(
            `Recovered public key does not match expected revocation public key for leaf ${leafIndex}`
          );
        }

        revocationKeys.push(recoveredPrivateKey);
      });

      // Finalize the token transaction with the keyshares
      this.finalizeTokenTransaction(
        finalTokenTransaction,
        revocationKeys,
        threshold
      );
    }

    return startResponse.finalTokenTransaction!;
  }

  async finalizeTokenTransaction(
    finalTokenTransaction: TokenTransaction,
    leafToSpendRevocationKeys: Uint8Array[],
    threshold: number
  ): Promise<TokenTransaction> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const signingOperatorResponse = await sparkClient.get_signing_operator_list(
      {}
    );

    // Submit finalize_token_transaction to all SOs in parallel
    const soResponses = await Promise.allSettled(
      Object.entries(signingOperatorResponse.signingOperators).map(
        async ([identifier, operator]) => {
          const internalSparkClient =
            await this.connectionManager.createSparkClient(operator.address);
          const response = await internalSparkClient.finalize_token_transaction(
            {
              finalTokenTransaction,
              leafToSpendRevocationKeys,
            }
          );

          return {
            identifier,
            response,
          };
        }
      )
    );

    // Count successful responses
    const successfulResponses = soResponses
      .filter(
        (
          result
        ): result is PromiseFulfilledResult<{
          identifier: string;
          response: TokenTransaction;
        }> => result.status === "fulfilled"
      )
      .map((result) => result.value);

    if (successfulResponses.length < threshold) {
      const errors = soResponses
        .filter(
          (result): result is PromiseRejectedResult =>
            result.status === "rejected"
        )
        .map((result) => result.reason)
        .join("\n");

      throw new Error(
        `Failed to collect enough successful finalization responses. Got ${successfulResponses.length}/${threshold} required.\nErrors:\n${errors}`
      );
    }

    // Return the finalized transaction
    return finalTokenTransaction;
  }
}
