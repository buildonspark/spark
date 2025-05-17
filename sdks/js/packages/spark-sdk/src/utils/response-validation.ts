import { NetworkError } from "../errors/index.js";

export function collectResponses<T>(responses: PromiseSettledResult<T>[]): T[] {
  // Get successful responses
  const successfulResponses = responses
    .filter(
      (result): result is PromiseFulfilledResult<T> =>
        result.status === "fulfilled",
    )
    .map((result) => result.value);

  // Get failed responses
  const failedResponses = responses.filter(
    (result): result is PromiseRejectedResult => result.status === "rejected",
  );

  if (failedResponses.length > 0) {
    const errors = failedResponses.map((result) => result.reason).join("\n");
    throw new NetworkError(
      `${failedResponses.length} out of ${responses.length} requests failed, please try again`,
      {
        errorCount: failedResponses.length,
        errors,
      },
    );
  }

  return successfulResponses;
}
