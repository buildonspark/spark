import {
  getDefaultUseTokenPrimitivesBindings,
  WalletConfig,
} from "../services/wallet-config.js";

describe("wallet config", () => {
  it("defaults to token primitive bindings outside React Native", () => {
    expect(getDefaultUseTokenPrimitivesBindings(false)).toBe(true);
    expect(WalletConfig.REGTEST.useTokenPrimitivesBindings).toBe(true);
  });

  it("keeps token primitive bindings disabled by default in React Native", () => {
    expect(getDefaultUseTokenPrimitivesBindings(true)).toBe(false);
  });
});
