import { jest } from "@jest/globals";

if (process.env.GITHUB_ACTIONS && process.env.HERMETIC_TEST) {
  jest.retryTimes(2, {
    logErrorsBeforeRetry: true,
  });
}
