/** @type {import('ts-jest').JestConfigWithTsJest} */
const config = {
  preset: "ts-jest/presets/default-esm",
  testEnvironment: "node",
  extensionsToTreatAsEsm: [".ts"],
  moduleNameMapper: {
    "^(\\.{1,2}/.*)\\.js$": "$1",
  },
  transform: {
    "^.+\\.js$": [
      "babel-jest",
      {
        presets: ["@babel/preset-env"],
        plugins: ["@babel/plugin-syntax-import-meta"],
      },
    ],
    "^.+\\.tsx?$": [
      "ts-jest",
      {
        tsconfig: "tsconfig-test.json",
        useESM: true,
      },
    ],
  },
};

module.exports = config;
