import { secp256k1 } from "@noble/curves/secp256k1";
import {
  TokenTransaction,
  OperatorSpecificTokenTransactionSignablePayload,
  OperatorSpecificTokenTransactionSignature,
  LeafWithPreviousTransactionData,
} from "../proto/spark";
import { ConnectionManager } from "./connection";
import { WalletConfigService } from "./config";
import {
  hashTokenTransaction,
  hashOperatorSpecificTokenTransactionSignablePayload,
} from "../utils/token-hashing";
import {
  getTokenLeavesSum,
  collectOwnedTokenLeafPublicKeys,
  calculateAvailableTokenAmount,
} from "../utils/token-transactions";
import { recoverPrivateKeyFromKeyshares } from "../utils/token-keyshares";
import { validateResponses } from "../utils/response-validation";
import {
  numberToBytesBE,
  bytesToHex,
  bytesToNumberBE,
} from "@noble/curves/abstract/utils";

const BURN_ADDRESS = new Uint8Array(32).fill(0x02);

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

  createMintTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint
  ): TokenTransaction {
    return {
      tokenInput: {
        $case: "mintInput",
        mintInput: {
          issuerPublicKey: tokenPublicKey,
          issuerProvidedTimestamp: Date.now(),
        },
      },
      outputLeaves: [
        {
          ownerPublicKey: tokenPublicKey,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
        },
      ],
      sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
    };
  }
  
  createTransferTokenTransaction(
    leavesToSpend: LeafWithPreviousTransactionData[],
    recipientPublicKey: Uint8Array,
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint
  ): TokenTransaction {
    return {
      tokenInput: {
        $case: "transferInput",
        transferInput: {
          leavesToSpend: leavesToSpend.map((leaf) => ({
            prevTokenTransactionHash: leaf.previousTransactionHash,
            prevTokenTransactionLeafVout: leaf.previousTransactionVout,
          })),
        },
      },
      outputLeaves: [
        {
          ownerPublicKey: recipientPublicKey,
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
        },
      ],
      sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
    };
  }
  
  collectOperatorIdentityPublicKeys(): Uint8Array[] {
    const operatorKeys: Uint8Array[] = [];
    for (const [_, operator] of Object.entries(this.config.getConfig().signingOperators)) {
      operatorKeys.push(operator.identityPublicKey);
    }
  
    return operatorKeys;
  }

  async broadcastTokenTransaction(
    tokenTransaction: TokenTransaction,
    // Not necessary if it's a mint transaction
    leafToSpendSigningPublicKeys?: Uint8Array[],
    leafToSpendRevocationPublicKeys?: Uint8Array[]
  ): Promise<TokenTransaction> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const signingOperators = this.config.getConfig().signingOperators;

    const partialTokenTransactionHash = hashTokenTransaction(
      tokenTransaction,
      true
    );

    const ownerSignatures: Uint8Array[] = [];
    if (tokenTransaction.tokenInput!.$case === "mintInput") {
      const issuerPublicKey =
        tokenTransaction.tokenInput!.mintInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new Error("issuer public key cannot be nil");
      }

      const owner_signature = await this.config.signer.signMessageWithPublicKey(
        partialTokenTransactionHash,
        issuerPublicKey
      );

      ownerSignatures.push(owner_signature);
    } else if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;

      if (!leafToSpendSigningPublicKeys || !leafToSpendRevocationPublicKeys) {
        throw new Error(
          "leafToSpendSigningPublicKeys and leafToSpendRevocationPublicKeys are required"
        );
      }

      for (let i = 0; i < transferInput.leavesToSpend.length; i++) {
        const leaf = transferInput.leavesToSpend[i];
        const ownerSignature = await this.config.signer.signMessageWithPublicKey(
          partialTokenTransactionHash,
          leafToSpendSigningPublicKeys![i]
        );

        ownerSignatures.push(ownerSignature);
      }
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
      Object.keys(signingOperators).length
    ) {
      throw new Error(
        `Keyshare operator count (${
          startResponse.keyshareInfo?.ownerIdentifiers.length
        }) does not match signing operator count (${
          Object.keys(signingOperators).length
        })`
      );
    }

    for (const identifier of startResponse.keyshareInfo?.ownerIdentifiers ||
      []) {
      if (!signingOperators[identifier]) {
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
      const issuerPublicKey =
        tokenTransaction.tokenInput!.mintInput.issuerPublicKey;
      if (!issuerPublicKey) {
        throw new Error("issuer public key cannot be nil");
      }

      const ownerSignature = await this.config.signer.signMessageWithPublicKey(
        payloadHash,
        issuerPublicKey
      );

      operatorSpecificSignatures.push({
        ownerPublicKey: issuerPublicKey,
        ownerSignature: ownerSignature,
        payload: payload,
      });
    }

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const transferInput = tokenTransaction.tokenInput!.transferInput;
      for (let i = 0; i < transferInput.leavesToSpend.length; i++) {
        const owner_signature =
          await this.config.signer.signMessageWithIdentityKey(payloadHash);

        operatorSpecificSignatures.push({
          ownerPublicKey: await this.config.signer.getIdentityPublicKey(),
          ownerSignature: owner_signature,
          payload: payload,
        });
      }
    }

    // Submit sign_token_transaction to all SOs in parallel and track their indices
    const soSignatures = await Promise.allSettled(
      Object.entries(signingOperators).map(
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
    const successfulSignatures = validateResponses(soSignatures);

    if (tokenTransaction.tokenInput!.$case === "transferInput") {
      const leavesToSpend =
        tokenTransaction.tokenInput!.transferInput.leavesToSpend;

      let revocationKeys: Uint8Array[] = [];

      for (let leafIndex = 0; leafIndex < leavesToSpend.length; leafIndex++) {
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
      }

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

    const signingOperators = this.config.getConfig().signingOperators;

    // Submit finalize_token_transaction to all SOs in parallel
    const soResponses = await Promise.allSettled(
      Object.entries(signingOperators).map(
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

    validateResponses(soResponses);

    return finalTokenTransaction;
  }

  async constructConsolidateTokenTransaction(
    tokenPublicKey: Uint8Array,
    selectedLeaves: LeafWithPreviousTransactionData[]
  ): Promise<TokenTransaction> {
    const tokenAmountSum = getTokenLeavesSum(selectedLeaves);

    const transferTokenTransaction: TokenTransaction = {
      tokenInput: {
        $case: "transferInput",
        transferInput: {
          leavesToSpend: selectedLeaves.map((leaf) => ({
            prevTokenTransactionHash: leaf.previousTransactionHash,
            prevTokenTransactionLeafVout: leaf.previousTransactionVout,
          })),
        },
      },
      outputLeaves: [
        {
          ownerPublicKey: await this.config.signer.generatePublicKey(),
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmountSum, 16),
        },
      ],
      sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
    };

    return transferTokenTransaction;
  }

  async constructBurnTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
    selectedLeaves: LeafWithPreviousTransactionData[]
  ) {
    const tokenAmountSum = getTokenLeavesSum(selectedLeaves);

    let transferTokenTransaction: TokenTransaction;

    if (tokenAmount > tokenAmountSum) {
      throw new Error("Not enough tokens to burn");
    } else if (tokenAmount === tokenAmountSum) {
      transferTokenTransaction = {
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: await this.config.signer.generatePublicKey(),
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmountSum, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
      };
    } else {
      const tokenDifferenceToSendBack = tokenAmountSum - tokenAmount;

      transferTokenTransaction = {
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: BURN_ADDRESS,
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmount, 16),
          },
          {
            ownerPublicKey: await this.config.signer.generatePublicKey(),
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenDifferenceToSendBack, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys: this.collectOperatorIdentityPublicKeys(),
      };
    }

    return transferTokenTransaction;
  }

  async fetchOwnedTokenLeaves(
    ownerPublicKeys: Uint8Array[],
    tokenPublicKeys: Uint8Array[]
  ): Promise<LeafWithPreviousTransactionData[]> {
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress()
    );

    const result = await sparkClient.get_owned_token_leaves({
      ownerPublicKeys,
      tokenPublicKeys,
    });

    return result.leavesWithPreviousTransactionData;
  }

  async syncTokenLeaves(
    tokenLeaves: Map<string, LeafWithPreviousTransactionData[]>
  ) {
    const unsortedTokenLeaves = await this.fetchOwnedTokenLeaves(
      await collectOwnedTokenLeafPublicKeys(this.config.signer),
      []
    );

    unsortedTokenLeaves.forEach((leaf) => {
      const tokenKey = bytesToHex(leaf.leaf!.tokenPublicKey!);
      const prevTxHashHex = bytesToHex(leaf.previousTransactionHash!);
      const index = leaf.previousTransactionVout!;

      const existingLeaves = tokenLeaves.get(tokenKey) || [];
      tokenLeaves.set(tokenKey, [{ ...leaf, previousTransactionVout: index }]);
    });
  }

  selectTokenLeaves(
    tokenLeaves: LeafWithPreviousTransactionData[],
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint
  ): LeafWithPreviousTransactionData[] {
    const tokenKey = bytesToHex(tokenPublicKey);

    if (calculateAvailableTokenAmount(tokenLeaves) < tokenAmount) {
      throw new Error("Insufficient available token amount");
    }

    // First try to find an exact match
    const exactMatch: LeafWithPreviousTransactionData | undefined =
      tokenLeaves.find(
        (item) => bytesToNumberBE(item.leaf!.tokenAmount!) === tokenAmount
      );

    if (exactMatch) {
      return [exactMatch];
    }

    // Sort by amount ascending for optimal selection.
    // It's in user's interest to hold as little leaves as possible,
    // so that in the event of a unilateral exit the fees are as low as possible
    tokenLeaves.sort((a, b) =>
      Number(
        bytesToNumberBE(a.leaf!.tokenAmount!) -
          bytesToNumberBE(b.leaf!.tokenAmount!)
      )
    );

    let remainingAmount = tokenAmount;
    const selectedLeaves: typeof tokenLeaves = [];

    // Select leaves using a greedy approach
    for (const leafInfo of tokenLeaves) {
      if (remainingAmount <= 0n) break;

      selectedLeaves.push(leafInfo);
      remainingAmount -= bytesToNumberBE(leafInfo.leaf!.tokenAmount!);
    }

    if (remainingAmount > 0n) {
      throw new Error("You do not have enough funds to complete the specified operation");
    }

    return selectedLeaves;
  }

  /**
   * Called after successful completion of a transaction to spend token leaves.
   * Allows wallet state to be updated without making an additional network call to sync token leaves.
   * @param tokenLeaves Current token leaves in memory for this tokenPublicKey
   * @param finalizedTokenTransaction Finalized transaction from either mint or transfer
   */
  updateTokenLeavesFromFinalizedTransaction(
    tokenLeaves: LeafWithPreviousTransactionData[],
    finalizedTokenTransaction: TokenTransaction
  ) {
    if (finalizedTokenTransaction.tokenInput!.$case === "transferInput") {
      finalizedTokenTransaction!.tokenInput!.transferInput!.leavesToSpend.forEach(
        (spentLeaf) => {
          const index = tokenLeaves.findIndex(
            (leaf) =>
              leaf.previousTransactionHash ===
                spentLeaf.prevTokenTransactionHash &&
              leaf.previousTransactionVout ===
                spentLeaf.prevTokenTransactionLeafVout
          );
          if (index !== -1) {
            tokenLeaves.splice(index, 1);
          }
        }
      );
    }

    const finalizedTokenTransactionHash = hashTokenTransaction(
      finalizedTokenTransaction,
      false
    );

    // Push the new leaves into the collection
    finalizedTokenTransaction!.outputLeaves!.forEach((outputLeaf, index) => {
      tokenLeaves.push({
        leaf: outputLeaf,
        previousTransactionHash: finalizedTokenTransactionHash,
        previousTransactionVout: index,
      });
    });
  }
}
