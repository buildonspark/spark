import { SparkReadonlyClient } from "@buildonspark/bare";
import process from "bare-process";

const port = Number(process.argv[2] || "45454");
const identityPublicKey =
  "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";
const sparkAddress =
  "sparkl1pgss9n9jdwnecca27cxfryhasa97xzr6arv8qvn4mu89tpcy5mf6fufjga67pf";
const coordinatorIdentifier = "local-coordinator";

const config = {
  network: "LOCAL",
  coordinatorIdentifier,
  signingOperators: {
    [coordinatorIdentifier]: {
      id: 0,
      identifier: coordinatorIdentifier,
      address: `http://127.0.0.1:${port}`,
      identityPublicKey,
    },
  },
};

const client = SparkReadonlyClient.createPublic(config);

async function runAttempt(label) {
  const startedAt = Date.now();
  try {
    const balance = await client.getAvailableBalance(sparkAddress);
    const elapsedMs = Date.now() - startedAt;
    console.log(
      JSON.stringify({
        label,
        ok: true,
        balance: balance.toString(),
        elapsedMs,
      }),
    );
  } catch (error) {
    const elapsedMs = Date.now() - startedAt;
    console.log(
      JSON.stringify({
        label,
        ok: false,
        elapsedMs,
        message: error instanceof Error ? error.message : String(error),
      }),
    );
  }
}

await runAttempt("first");
await runAttempt("second");
