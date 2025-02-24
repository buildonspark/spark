export function validateResponses(responses) {
    // Get successful responses
    const successfulResponses = responses
        .filter((result) => result.status === "fulfilled")
        .map((result) => result.value);
    // If no successful responses, throw with all errors
    if (successfulResponses.length === 0) {
        const errors = responses
            .filter((result) => result.status === "rejected")
            .map((result) => result.reason)
            .join("\n");
        throw new Error(`All requests failed.\nErrors:\n${errors}`);
    }
    return successfulResponses;
}
//# sourceMappingURL=response-validation.js.map