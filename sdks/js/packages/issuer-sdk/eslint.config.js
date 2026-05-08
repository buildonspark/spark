import base from "@lightsparkdev/eslint-config/base";

export default [
  {
    // Stage issuer-sdk lint in the same sequence as spark-sdk: generated files stay ignored,
    // runtime-affecting fixes and test cleanup land in follow-up PRs.
    ignores: ["src/proto/**"],
  },
  ...base,
];
