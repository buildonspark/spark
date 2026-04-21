import { jest } from "@jest/globals";

if (process.env.GITHUB_ACTIONS && process.env.SPARK_LOCAL_INGRESS_HOST) {
  jest.retryTimes(5, {
    logErrorsBeforeRetry: true,
  });
}
