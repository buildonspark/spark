import base from "@lightsparkdev/eslint-config/base";

export default [
  ...base,
  {
    files: ["src/index.cjs", "tests/**/*.js"],
    rules: {
      "@typescript-eslint/no-require-imports": "off",
    },
  },
];
