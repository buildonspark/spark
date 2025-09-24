import { jest } from "@jest/globals";

if (process.env.GITHUB_ACTIONS && process.env.MINIKUBE_IP) {
  jest.retryTimes(5, {
    logErrorsBeforeRetry: true,
  });
}
