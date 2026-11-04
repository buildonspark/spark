import { jest } from "@jest/globals";

const runtime = {
  isNode: false,
  isBare: false,
};

jest.unstable_mockModule("@lightsparkdev/core", () => runtime);

describe("client environment", () => {
  const globals = ["navigator", "chrome", "window", "location", "Bun", "Bare"];
  const descriptors = globals.map((name) =>
    Object.getOwnPropertyDescriptor(globalThis, name),
  );

  beforeEach(() => {
    jest.resetModules();
    runtime.isNode = false;
    runtime.isBare = false;
  });

  afterEach(() => {
    for (const [index, name] of globals.entries()) {
      const descriptor = descriptors[index];
      if (descriptor) {
        Object.defineProperty(globalThis, name, descriptor);
      } else {
        Reflect.deleteProperty(globalThis, name);
      }
    }
  });

  function setGlobal(name: string, value: unknown) {
    Object.defineProperty(globalThis, name, {
      configurable: true,
      value,
    });
  }

  function setNavigator(value: Record<string, unknown>) {
    setGlobal("navigator", value);
  }

  const windowsChrome =
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";

  it.each([
    [windowsChrome, "chrome-131.0.0.0/windows-NT 10.0"],
    [`${windowsChrome} Electron/33.0.0`, "electron-33.0.0/windows-NT 10.0"],
    [
      `${windowsChrome} Vivaldi/7.0.3495.6`,
      "vivaldi-7.0.3495.6/windows-NT 10.0",
    ],
    [
      `${windowsChrome} Edg/131.0.2903.86`,
      "microsoft-edge-131.0.2903.86/windows-NT 10.0",
    ],
    [`${windowsChrome} OPR/116.0.0.0`, "opera-116.0.0.0/windows-NT 10.0"],
    [
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
      "safari-18.0/macos-10.15.7",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
      "safari-18.0/ios-18.0",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
      "webkit-605.1.15/ios-18.0",
    ],
    [
      "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1",
      "chrome-131.0.6778.73/ios-18.0",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/132.0 Mobile/15E148 Safari/605.1.15",
      "firefox-132.0/ios-18.0",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 EdgiOS/131.0.2903.77 Mobile/15E148 Safari/605.1.15",
      "microsoft-edge-131.0.2903.77/ios-18.0",
    ],
    [
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
      "chrome-131.0.0.0/android-14",
    ],
    [
      "Mozilla/5.0 (Linux; Android 14; Pixel 8 Build/UP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/131.0.0.0 Mobile Safari/537.36",
      "chrome-webview-131.0.0.0/android-14",
    ],
    [
      "Mozilla/5.0 (Linux; Android 14; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/27.0 Chrome/125.0.0.0 Mobile Safari/537.36",
      "samsung-internet-for-android-27.0/android-14",
    ],
    [
      "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
      "firefox-132.0/linux-unknown-version",
    ],
    [
      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chromium/131.0.0.0 Safari/537.36",
      "chromium-131.0.0.0/linux-unknown-version",
    ],
    [
      "Mozilla/5.0 (X11; CrOS x86_64 16093.68.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
      "chrome-131.0.0.0/chrome-os-unknown-version",
    ],
    [
      "Mozilla/5.0 (Linux; Android) AppleWebKit/537.36",
      "webkit-537.36/android-unknown-version",
    ],
  ])("includes browser and OS details: %s", async (userAgent, details) => {
    setNavigator({ userAgent });
    const { getClientEnv, packageVersion } = await import("../constants.js");

    expect(getClientEnv()).toBe(
      `js-spark-sdk/${packageVersion} browser/${details}`,
    );
  });

  it("uses fallback details for an unrecognized user agent", async () => {
    setNavigator({ userAgent: "unrecognized" });
    const { getClientEnv } = await import("../constants.js");

    expect(getClientEnv()).toContain(
      "browser/unknown-browser-unknown-version/unknown-os-unknown-version",
    );
  });

  it.each(["", undefined, null, 42])(
    "handles user agent %p",
    async (userAgent) => {
      setNavigator({ userAgent });
      const { getClientEnv } = await import("../constants.js");

      expect(getClientEnv()).toContain("browser/unknown-user-agent");
    },
  );

  it("handles a missing user agent", async () => {
    setNavigator({});
    const { getClientEnv } = await import("../constants.js");

    expect(getClientEnv()).toContain("browser/unknown-user-agent");
  });

  it("preserves React Native environment details without a user agent", async () => {
    setNavigator({ product: "ReactNative" });
    const { getClientEnv, setReactNativeEnvDetails, packageVersion } =
      await import("../constants.js");
    setReactNativeEnvDetails("0.79.4", "ios", "18.0");

    expect(getClientEnv()).toBe(
      `js-spark-sdk/${packageVersion} react-native/0.79.4/ios-18.0`,
    );
  });

  it.each([true, false])(
    "includes extension context (content script: %s)",
    async (contentScript) => {
      setNavigator({ userAgent: windowsChrome });
      setGlobal("chrome", { runtime: { id: "test-extension" } });
      setGlobal("location", { protocol: "chrome-extension:" });
      if (contentScript) {
        setGlobal("window", {});
      }
      const { getClientEnv, packageVersion } = await import("../constants.js");

      expect(getClientEnv()).toBe(
        `js-spark-sdk/${packageVersion} web-extension/chrome-extension/${contentScript ? "content-script" : "background-script"}/chrome-131.0.0.0/windows-NT 10.0`,
      );
    },
  );

  it("preserves Node environment details when navigator is present", async () => {
    runtime.isNode = true;
    setNavigator({ userAgent: "Node.js/24" });
    const { getClientEnv, packageVersion } = await import("../constants.js");

    expect(getClientEnv()).toBe(
      `js-spark-sdk/${packageVersion} node/${process.version}`,
    );
  });

  it("preserves Bun environment details ahead of Node", async () => {
    runtime.isNode = true;
    setGlobal("Bun", { version: "1.2.0" });
    const { getClientEnv, packageVersion } = await import("../constants.js");

    expect(getClientEnv()).toBe(`js-spark-sdk/${packageVersion} bun/1.2.0`);
  });

  it("preserves Bare environment details without navigator", async () => {
    runtime.isBare = true;
    Reflect.deleteProperty(globalThis, "navigator");
    setGlobal("Bare", { version: "1.16.0" });
    const { getClientEnv, packageVersion } = await import("../constants.js");

    expect(getClientEnv()).toBe(`js-spark-sdk/${packageVersion} bare/1.16.0`);
  });
});
