/** @type {import('ts-jest').JestConfigWithTsJest} */
const config = {
  preset: "ts-jest/presets/default-esm",
  testEnvironment: "node",
  extensionsToTreatAsEsm: [".ts"],
  setupFiles: ["<rootDir>/jest/environment-setup.ts"],
  setupFilesAfterEnv: ["<rootDir>/jest/test-setup.ts"],
  moduleNameMapper: {
    "^(\\.{1,2}/.*)\\.js$": "$1",
  },
};

module.exports = config;
