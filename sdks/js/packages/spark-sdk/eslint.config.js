import base from "@lightsparkdev/eslint-config/base";

export default [
  {
    // Stage the first lint rollout without downgrading the shared base rules.
    // These dynamic boundaries need dedicated type cleanup before opt-in.
    ignores: [
      "src/graphql/objects/**",
      "src/proto/**",
      "src/spark-bindings/wasm/**",
      "src/tests/**",
      "src/token-primitives-bindings/wasm/**",
      "src/wasm/**",
      "wasm/**",
    ],
  },
  ...base,
];
