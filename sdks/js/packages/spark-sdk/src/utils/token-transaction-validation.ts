import { InternalValidationError } from "../errors/types.js";
import {
  TokenTransaction as TokenTransaction,
  TokenOutputToSpend as TokenOutputToSpend,
} from "../proto/spark_token.js";

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
  keyshareInfo: { ownerIdentifiers: string[]; threshold: number },
  expectedWithdrawBondSats: number,
  expectedWithdrawRelativeBlockLocktime: number,
  expectedThreshold: number,
) {
  if (finalTokenTransaction.network !== partialTokenTransaction.network) {
    throw new InternalValidationError(
      "Network mismatch in response token transaction",
      {
        value: finalTokenTransaction.network,
        expected: partialTokenTransaction.network,
      },
    );
  }

  if (!finalTokenTransaction.tokenInputs) {
    throw new InternalValidationError(
      "Token inputs missing in final transaction",
      {
        value: finalTokenTransaction,
      },
    );
  }

  if (!partialTokenTransaction.tokenInputs) {
    throw new InternalValidationError(
      "Token inputs missing in partial transaction",
      {
        value: partialTokenTransaction,
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
        value: finalTokenTransaction.tokenInputs.$case,
        expected: partialTokenTransaction.tokenInputs.$case,
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
        value: finalTokenTransaction.sparkOperatorIdentityPublicKeys.length,
        expected:
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

    if (!finalMintInput.issuerPublicKey || !partialMintInput.issuerPublicKey) {
      throw new InternalValidationError(
        "Issuer public key missing in mint input",
        {
          value: finalMintInput.issuerPublicKey,
          expected: partialMintInput.issuerPublicKey,
        },
      );
    }
    if (!finalMintInput.tokenIdentifier || !partialMintInput.tokenIdentifier) {
      throw new InternalValidationError(
        "Token identifier missing in mint input",
        {
          value: finalMintInput.tokenIdentifier,
          expected: partialMintInput.tokenIdentifier,
        },
      );
    }

    if (
      !areByteArraysEqual(
        finalMintInput.issuerPublicKey,
        partialMintInput.issuerPublicKey,
      )
    ) {
      throw new InternalValidationError(
        "Issuer public key mismatch in mint input",
        {
          value: finalMintInput.issuerPublicKey.toString(),
          expected: partialMintInput.issuerPublicKey.toString(),
        },
      );
    }

    if (
      !areByteArraysEqual(
        finalMintInput.tokenIdentifier,
        partialMintInput.tokenIdentifier,
      )
    ) {
      throw new InternalValidationError(
        "Issuer public key mismatch in mint input",
        {
          value: finalMintInput.tokenIdentifier.toString(),
          expected: partialMintInput.tokenIdentifier.toString(),
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
          value: finalTransferInput.outputsToSpend.length,
          expected: partialTransferInput.outputsToSpend.length,
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
            value: finalOutput,
          },
        );
      }

      if (!partialOutput) {
        throw new InternalValidationError(
          "Token output to spend missing in partial transaction",
          {
            outputIndex: i,
            value: partialOutput,
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
            value: finalOutput.prevTokenTransactionHash.toString(),
            expected: partialOutput.prevTokenTransactionHash.toString(),
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
            value: finalOutput.prevTokenTransactionVout,
            expected: partialOutput.prevTokenTransactionVout,
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
      value: finalTokenTransaction.tokenOutputs.length,
      expected: partialTokenTransaction.tokenOutputs.length,
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
          value: finalOutput,
        },
      );
    }

    if (!partialOutput) {
      throw new InternalValidationError(
        "Token output missing in partial transaction",
        {
          outputIndex: i,
          value: partialOutput,
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
          value: finalOutput.ownerPublicKey.toString(),
          expected: partialOutput.ownerPublicKey.toString(),
        },
      );
    }

    if (
      finalOutput.tokenPublicKey !== undefined &&
      partialOutput.tokenPublicKey !== undefined &&
      !areByteArraysEqual(
        finalOutput.tokenPublicKey!,
        partialOutput.tokenPublicKey!,
      )
    ) {
      throw new InternalValidationError(
        "Token public key mismatch in token output",
        {
          outputIndex: i,
          value: finalOutput.tokenPublicKey!.toString(),
          expected: partialOutput.tokenPublicKey!.toString(),
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
          value: finalOutput.tokenAmount.toString(),
          expected: partialOutput.tokenAmount.toString(),
        },
      );
    }

    if (finalOutput.withdrawBondSats !== undefined) {
      if (finalOutput.withdrawBondSats !== expectedWithdrawBondSats) {
        throw new InternalValidationError(
          "Withdraw bond sats mismatch in token output",
          {
            outputIndex: i,
            value: finalOutput.withdrawBondSats,
            expected: expectedWithdrawBondSats,
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
            value: finalOutput.withdrawRelativeBlockLocktime,
            expected: expectedWithdrawRelativeBlockLocktime,
          },
        );
      }
    }

    if (keyshareInfo.threshold !== expectedThreshold) {
      throw new InternalValidationError(
        "Threshold mismatch: expected " +
          expectedThreshold +
          " but got " +
          keyshareInfo.threshold,
        {
          field: "threshold",
          value: keyshareInfo.threshold,
          expected: expectedThreshold,
        },
      );
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

  if (
    finalTokenTransaction.clientCreatedTimestamp!.getTime() !==
    partialTokenTransaction.clientCreatedTimestamp!.getTime()
  ) {
    throw new InternalValidationError("Client created timestamp mismatch", {
      value: finalTokenTransaction.clientCreatedTimestamp,
      expected: partialTokenTransaction.clientCreatedTimestamp,
    });
  }
}
