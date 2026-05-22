import base from "@lightsparkdev/eslint-config/base";

export default [
  ...base,
  {
    files: ["**/*.js"],
    rules: {
      "@typescript-eslint/no-require-imports": "off",
    },
  },
];
