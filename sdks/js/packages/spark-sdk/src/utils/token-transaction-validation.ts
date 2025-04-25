import { InternalValidationError } from "../errors/types.js";
import { TokenTransaction, TokenOutputToSpend } from "../proto/spark.js";

function areByteArraysEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) {
    return false;
  }
  return a.every((byte, index) => byte === b[index]);
}

function hasDuplicates<T>(array: T[]): boolean {
  return new Set(array).size !== array.length;
}

export function validateTokenTransaction(
  finalTokenTransaction: TokenTransaction,
  partialTokenTransaction: TokenTransaction,
  signingOperators: Record<string, any>,
  keyshareInfo: { ownerIdentifiers: string[] },
  expectedWithdrawBondSats: number,
  expectedWithdrawRelativeBlockLocktime: number,
) {
  if (finalTokenTransaction.network !== partialTokenTransaction.network) {
    throw new InternalValidationError(
      "Network mismatch in response token transaction",
      {
        finalTransaction: finalTokenTransaction.network,
        partialTransaction: partialTokenTransaction.network,
      },
    );
  }

  if (!finalTokenTransaction.tokenInputs) {
    throw new InternalValidationError(
      "Token inputs missing in final transaction",
      {
        finalTransaction: finalTokenTransaction,
      },
    );
  }

  if (!partialTokenTransaction.tokenInputs) {
    throw new InternalValidationError(
      "Token inputs missing in partial transaction",
      {
        partialTransaction: partialTokenTransaction,
      },
    );
  }

  if (
    finalTokenTransaction.tokenInputs.$case !==
    partialTokenTransaction.tokenInputs.$case
  ) {
    throw new InternalValidationError(
      `Transaction type mismatch: final transaction has ${finalTokenTransaction.tokenInputs.$case}, partial transaction has ${partialTokenTransaction.tokenInputs.$case}`,
      {
        finalTransaction: finalTokenTransaction.tokenInputs.$case,
        partialTransaction: partialTokenTransaction.tokenInputs.$case,
      },
    );
  }

  if (
    finalTokenTransaction.sparkOperatorIdentityPublicKeys.length !==
    partialTokenTransaction.sparkOperatorIdentityPublicKeys.length
  ) {
    throw new InternalValidationError(
      "Spark operator identity public keys count mismatch",
      {
        finalTransaction:
          finalTokenTransaction.sparkOperatorIdentityPublicKeys.length,
        partialTransaction:
          partialTokenTransaction.sparkOperatorIdentityPublicKeys.length,
      },
    );
  }

  if (
    partialTokenTransaction.tokenInputs.$case === "mintInput" &&
    finalTokenTransaction.tokenInputs.$case === "mintInput"
  ) {
    const finalMintInput = finalTokenTransaction.tokenInputs.mintInput;
    const partialMintInput = partialTokenTransaction.tokenInputs.mintInput;

    if (
      !areByteArraysEqual(
        finalMintInput.issuerPublicKey,
        partialMintInput.issuerPublicKey,
      )
    ) {
      throw new InternalValidationError(
        "Issuer public key mismatch in mint input",
        {
          finalTransaction: finalMintInput.issuerPublicKey.toString(),
          partialTransaction: partialMintInput.issuerPublicKey.toString(),
        },
      );
    }
  } else if (
    partialTokenTransaction.tokenInputs.$case === "transferInput" &&
    finalTokenTransaction.tokenInputs.$case === "transferInput"
  ) {
    const finalTransferInput = finalTokenTransaction.tokenInputs.transferInput;
    const partialTransferInput =
      partialTokenTransaction.tokenInputs.transferInput;

    if (
      finalTransferInput.outputsToSpend.length !==
      partialTransferInput.outputsToSpend.length
    ) {
      throw new InternalValidationError(
        "Outputs to spend count mismatch in transfer input",
        {
          finalTransaction: finalTransferInput.outputsToSpend.length,
          partialTransaction: partialTransferInput.outputsToSpend.length,
        },
      );
    }

    for (let i = 0; i < finalTransferInput.outputsToSpend.length; i++) {
      const finalOutput = finalTransferInput.outputsToSpend[
        i
      ] as TokenOutputToSpend;
      const partialOutput = partialTransferInput.outputsToSpend[
        i
      ] as TokenOutputToSpend;

      if (!finalOutput) {
        throw new InternalValidationError(
          "Token output to spend missing in final transaction",
          {
            outputIndex: i,
            finalTransaction: finalOutput,
          },
        );
      }

      if (!partialOutput) {
        throw new InternalValidationError(
          "Token output to spend missing in partial transaction",
          {
            outputIndex: i,
            partialTransaction: partialOutput,
          },
        );
      }

      if (
        !areByteArraysEqual(
          finalOutput.prevTokenTransactionHash,
          partialOutput.prevTokenTransactionHash,
        )
      ) {
        throw new InternalValidationError(
          "Previous token transaction hash mismatch in transfer input",
          {
            outputIndex: i,
            finalTransaction: finalOutput.prevTokenTransactionHash.toString(),
            partialTransaction:
              partialOutput.prevTokenTransactionHash.toString(),
          },
        );
      }

      if (
        finalOutput.prevTokenTransactionVout !==
        partialOutput.prevTokenTransactionVout
      ) {
        throw new InternalValidationError(
          "Previous token transaction vout mismatch in transfer input",
          {
            outputIndex: i,
            finalTransaction: finalOutput.prevTokenTransactionVout,
            partialTransaction: partialOutput.prevTokenTransactionVout,
          },
        );
      }
    }
  }

  if (
    finalTokenTransaction.tokenOutputs.length !==
    partialTokenTransaction.tokenOutputs.length
  ) {
    throw new InternalValidationError("Token outputs count mismatch", {
      finalTransaction: finalTokenTransaction.tokenOutputs.length,
      partialTransaction: partialTokenTransaction.tokenOutputs.length,
    });
  }

  for (let i = 0; i < finalTokenTransaction.tokenOutputs.length; i++) {
    const finalOutput = finalTokenTransaction.tokenOutputs[i];
    const partialOutput = partialTokenTransaction.tokenOutputs[i];

    if (!finalOutput) {
      throw new InternalValidationError(
        "Token output missing in final transaction",
        {
          outputIndex: i,
          finalTransaction: finalOutput,
        },
      );
    }

    if (!partialOutput) {
      throw new InternalValidationError(
        "Token output missing in partial transaction",
        {
          outputIndex: i,
          partialTransaction: partialOutput,
        },
      );
    }

    if (
      !areByteArraysEqual(
        finalOutput.ownerPublicKey,
        partialOutput.ownerPublicKey,
      )
    ) {
      throw new InternalValidationError(
        "Owner public key mismatch in token output",
        {
          outputIndex: i,
          finalTransaction: finalOutput.ownerPublicKey.toString(),
          partialTransaction: partialOutput.ownerPublicKey.toString(),
        },
      );
    }

    if (
      !areByteArraysEqual(
        finalOutput.tokenPublicKey,
        partialOutput.tokenPublicKey,
      )
    ) {
      throw new InternalValidationError(
        "Token public key mismatch in token output",
        {
          outputIndex: i,
          finalTransaction: finalOutput.tokenPublicKey.toString(),
          partialTransaction: partialOutput.tokenPublicKey.toString(),
        },
      );
    }

    if (
      !areByteArraysEqual(finalOutput.tokenAmount, partialOutput.tokenAmount)
    ) {
      throw new InternalValidationError(
        "Token amount mismatch in token output",
        {
          outputIndex: i,
          finalTransaction: finalOutput.tokenAmount.toString(),
          partialTransaction: partialOutput.tokenAmount.toString(),
        },
      );
    }

    if (finalOutput.withdrawBondSats !== undefined) {
      if (finalOutput.withdrawBondSats !== expectedWithdrawBondSats) {
        throw new InternalValidationError(
          "Withdraw bond sats mismatch in token output",
          {
            outputIndex: i,
            finalTransaction: finalOutput.withdrawBondSats,
            expectedValue: expectedWithdrawBondSats,
          },
        );
      }
    }

    if (finalOutput.withdrawRelativeBlockLocktime !== undefined) {
      if (
        finalOutput.withdrawRelativeBlockLocktime !==
        expectedWithdrawRelativeBlockLocktime
      ) {
        throw new InternalValidationError(
          "Withdraw relative block locktime mismatch in token output",
          {
            outputIndex: i,
            finalTransaction: finalOutput.withdrawRelativeBlockLocktime,
            expectedValue: expectedWithdrawRelativeBlockLocktime,
          },
        );
      }
    }
  }

  if (
    keyshareInfo.ownerIdentifiers.length !==
    Object.keys(signingOperators).length
  ) {
    throw new InternalValidationError(
      `Keyshare operator count (${keyshareInfo.ownerIdentifiers.length}) does not match signing operator count (${Object.keys(signingOperators).length})`,
      {
        keyshareInfo: keyshareInfo.ownerIdentifiers.length,
        signingOperators: Object.keys(signingOperators).length,
      },
    );
  }

  if (hasDuplicates(keyshareInfo.ownerIdentifiers)) {
    throw new InternalValidationError(
      "Duplicate ownerIdentifiers found in keyshareInfo",
      {
        keyshareInfo: keyshareInfo.ownerIdentifiers,
      },
    );
  }

  for (const identifier of keyshareInfo.ownerIdentifiers) {
    if (!signingOperators[identifier]) {
      throw new InternalValidationError(
        `Keyshare operator ${identifier} not found in signing operator list`,
        {
          keyshareInfo: identifier,
          signingOperators: Object.keys(signingOperators),
        },
      );
    }
  }
}
