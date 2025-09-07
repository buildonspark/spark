// @ts-nocheck
import { BitcoinFaucet } from "./bitcoin-faucet";

let processInstance: BitcoinFaucet | null = null;

export function getBitcoinFaucet(): BitcoinFaucet {
  if (!processInstance) {
    console.log(
      `[bitcoin-faucet-wrapper] Creating new BitcoinFaucet instance for process ${process.pid}`,
    );

    const url = process.env.BITCOIN_RPC_URL || "http://127.0.0.1:8332";
    const username = process.env.BITCOIN_RPC_USER || "testutil";
    const password = process.env.BITCOIN_RPC_PASSWORD || "testutilpassword";

    processInstance = BitcoinFaucet.getInstance(url, username, password);

    if (!processInstance) {
      throw new Error(
        `Failed to create BitcoinFaucet instance in process ${process.pid}`,
      );
    }

    console.log(
      `[bitcoin-faucet-wrapper] BitcoinFaucet instance created successfully`,
    );
  }

  return processInstance;
}

export async function sendToAddress(
  address: string,
  amount: bigint,
): Promise<string> {
  const faucet = getBitcoinFaucet();
  console.log(
    `[bitcoin-faucet-wrapper] Calling sendToAddress for ${address} with ${amount} sats`,
  );
  return await faucet.sendToAddress(address, amount);
}

export async function mineBlocks(blocks: number): Promise<string[]> {
  const faucet = getBitcoinFaucet();
  return await faucet.mineBlocks(blocks);
}

export async function fundWalletFromGraphQL(
  bitcoinAddress: string,
  amountSats: number,
  options?: {
    onRateLimit?: () => Promise<boolean>; // Callback to check if we should skip the faucet
  },
): Promise<string> {
  const requestBody = {
    operationName: "ArtilleryFaucet",
    variables: {
      amount_sats: amountSats,
      bitcoin_address: bitcoinAddress,
    },
    query:
      "mutation ArtilleryFaucet($amount_sats: Long!, $bitcoin_address: String!) { request_regtest_funds( input: { amount_sats: $amount_sats, address: $bitcoin_address } ) { transaction_hash } }",
  };

  console.log(
    `[bitcoin-faucet-wrapper] Funding wallet ${bitcoinAddress} with ${amountSats} sats via GraphQL`,
  );

  const maxRetries = 5;
  const baseDelay = 30000; // Start with 30 seconds
  const maxDelay = 60000; // Max 60 seconds

  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    try {
      const authId = process.env.GRAPHQL_FAUCET_ID;
      const authSecret = process.env.GRAPHQL_FAUCET_SECRET;
      const faucetUrl = process.env.GRAPHQL_FAUCET_URL;

      if (!authId || !authSecret) {
        throw new Error(
          "GRAPHQL_FAUCET_ID, GRAPHQL_FAUCET_SECRET and GRAPHQL_FAUCET_URL environment variables must be set",
        );
      }
      const basicAuth = Buffer.from(`${authId}:${authSecret}`).toString(
        "base64",
      );

      const headers = {
        "Content-Type": "application/json",
        Authorization: `Basic ${basicAuth}`,
      };

      const response = await fetch(faucetUrl, {
        method: "POST",
        headers: headers,
        body: JSON.stringify(requestBody),
      });

      const responseText = await response.text();

      if (!response.ok) {
        // Check if it's a rate limit error
        if (response.status === 429) {
          const delay = Math.min(
            baseDelay * Math.pow(2, attempt - 1),
            maxDelay,
          );
          console.warn(
            `[bitcoin-faucet-wrapper] Rate limited (429). Attempt ${attempt}/${maxRetries}. Retrying in ${delay}ms...`,
          );

          if (attempt < maxRetries) {
            await new Promise((resolve) => setTimeout(resolve, delay));
            continue;
          }
        }
        throw new Error(
          `GraphQL request failed with status ${response.status}: ${responseText}`,
        );
      }

      const data = JSON.parse(responseText);

      if (data.errors) {
        // Check if any error is a rate limit error
        const hasRateLimitError = data.errors.some(
          (error) =>
            error.message?.includes("429") ||
            error.message?.includes("Rate limited") ||
            error.extensions?.error_name === "RateLimitException",
        );

        if (hasRateLimitError && attempt < maxRetries) {
          const delay = Math.min(
            baseDelay * Math.pow(2, attempt - 1),
            maxDelay,
          );
          console.warn(
            `[bitcoin-faucet-wrapper] GraphQL rate limit error. Attempt ${attempt}/${maxRetries}. Retrying in ${delay}ms...`,
          );
          await new Promise((resolve) => setTimeout(resolve, delay));
          continue;
        }

        throw new Error(`GraphQL errors: ${JSON.stringify(data.errors)}`);
      }

      const transactionHash =
        data.data?.request_regtest_funds?.transaction_hash;

      if (!transactionHash) {
        throw new Error("No transaction hash returned from GraphQL faucet");
      }

      console.log(
        `[bitcoin-faucet-wrapper] Successfully funded wallet via GraphQL, transaction hash: ${transactionHash}`,
      );
      return transactionHash;
    } catch (error) {
      if (attempt === maxRetries) {
        console.error(
          `[bitcoin-faucet-wrapper] Failed to fund wallet via GraphQL after ${maxRetries} attempts:`,
          error,
        );
        throw error;
      }
      // For non-rate-limit errors on non-final attempts, still retry with backoff
      const delay = Math.min(baseDelay * Math.pow(2, attempt - 1), maxDelay);
      console.warn(
        `[bitcoin-faucet-wrapper] Error on attempt ${attempt}/${maxRetries}. Retrying in ${delay}ms...`,
        error.message,
      );
      await new Promise((resolve) => setTimeout(resolve, delay));
    }
  }
}
