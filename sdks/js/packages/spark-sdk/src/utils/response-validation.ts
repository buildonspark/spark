import { SparkRequestError } from "../errors/index.js";

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
    const reasons = failedResponses.map(
      (result): unknown => result.reason as unknown,
    );
    const errors = reasons.map(String).join("\n");
    throw new SparkRequestError(
      `${failedResponses.length} out of ${responses.length} requests failed, please try again`,
      {
        errorCount: failedResponses.length,
        errors,
        error: reasons,
      },
    );
  }

  return successfulResponses;
}
