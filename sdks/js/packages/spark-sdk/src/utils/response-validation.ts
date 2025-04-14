export function validateResponses<T>(
  responses: PromiseSettledResult<T>[],
): T[] {
  // Get successful responses
  const successfulResponses = responses
    .filter(
      (result): result is PromiseFulfilledResult<T> =>
        result.status === "fulfilled",
    )
    .map((result) => result.value);

  // If no successful responses, throw with all errors
  if (successfulResponses.length === 0) {
    const errors = responses
      .filter(
        (result): result is PromiseRejectedResult =>
          result.status === "rejected",
      )
      .map((result) => result.reason)
      .join("\n");

    throw new Error(`All requests failed.\nErrors:\n${errors}`);
  }

  return successfulResponses;
}
