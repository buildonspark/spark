import { isNode, isBare } from "@lightsparkdev/core";

type BunGlobal = {
  version?: unknown;
};
type ChromeGlobal = {
  runtime?: {
    id?: unknown;
  };
};
type BareGlobal = {
  version?: unknown;
};

const globalRecord = globalThis as Record<string, unknown>;

export const isReactNative =
  "navigator" in globalThis && navigator.product === "ReactNative";

export const isBun = "Bun" in globalThis;

export const isWebExtension =
  /**
   * globalThis.chrome actually exists in extension contexts for all browsers for legacy reasons:
   */
  "chrome" in globalThis &&
  typeof (globalRecord.chrome as ChromeGlobal).runtime?.id === "string";
export const isWebExtensionContentScript =
  isWebExtension && "window" in globalThis;
export const isWebExtensionBackgroundScript =
  isWebExtension && !isWebExtensionContentScript;

/* navigator.userAgent exists in browsers and extension contexts: */
const userAgent =
  "navigator" in globalThis
    ? parseUserAgent(globalThis.navigator.userAgent)
    : undefined;

declare const __PACKAGE_VERSION__: string;

export const packageVersion =
  typeof __PACKAGE_VERSION__ !== "undefined" ? __PACKAGE_VERSION__ : "unknown";

let baseEnvStr = "unknown";
if (isBun) {
  const bun = globalRecord.Bun as BunGlobal;
  const bunVersion =
    typeof bun.version === "string" ? bun.version : "unknown-version";
  baseEnvStr = `bun/${bunVersion}`;
} else if (isNode) {
  baseEnvStr = `node/${globalThis.process.version}`;
} else if (isReactNative) {
  baseEnvStr = "react-native";
} else if (isBare) {
  const bare = Bare as BareGlobal;
  const bareVersion =
    typeof bare.version === "string" ? bare.version : "unknown-version";
  baseEnvStr = `bare/${bareVersion}`;
} else if (isWebExtension) {
  /* Protocol may contain additional information about where the
     extension is running, e.g. chrome-extension: or moz-extension: */
  const protocol = "location" in globalThis ? globalThis.location.protocol : "";
  const extScriptType = isWebExtensionContentScript
    ? "content-script"
    : "background-script";
  baseEnvStr = `web-extension/${protocol.replace(":", "")}/${extScriptType}/${userAgent}`;
} else {
  baseEnvStr = `browser/${userAgent}`;
}

function parseUserAgent(userAgent: unknown) {
  if (typeof userAgent !== "string" || !userAgent) {
    return "unknown-user-agent";
  }

  // Diagnostic labels only: more specific browsers must precede their
  // compatibility tokens (e.g. Edge also advertises Chrome and Safari).
  const browser = parseUserAgentPart(userAgent, "unknown-browser", [
    ["electron", /\bElectron\/([\d.]+)/i],
    ["vivaldi", /\bVivaldi\/([\d.]+)/i],
    ["microsoft-edge", /\b(?:EdgA|EdgiOS|Edg|Edge)\/([\d.]+)/i],
    ["opera", /\b(?:OPR|OPiOS)\/([\d.]+)/i],
    ["samsung-internet-for-android", /\bSamsungBrowser\/([\d.]+)/i],
    ["firefox", /\b(?:Firefox|FxiOS)\/([\d.]+)/i],
    ["chrome-webview", /\bAndroid\b.*;\s*wv\b.*\bChrome\/([\d.]+)/i],
    ["chromium", /\bChromium\/([\d.]+)/i],
    ["chrome", /\b(?:Chrome|CriOS)\/([\d.]+)/i],
    ["safari", /\bVersion\/([\d.]+).*\bSafari\//i],
    // Embedded WebKit clients may omit browser tokens; retain the engine version.
    ["webkit", /\bAppleWebKit\/([\d.]+)/i],
  ]);
  const os = parseUserAgentPart(userAgent, "unknown-os", [
    ["windows", /\bWindows (NT [\d.]+)/i],
    ["ios", /\b(?:iPhone|iPad|iPod).*?\bOS ([\d_]+)/i],
    ["android", /\bAndroid(?:[ /]([\d.]+))?/i],
    ["chrome-os", /\bCrOS\b/i],
    ["macos", /\bMac OS X(?:[ /]([\d_.]+))?/i],
    ["linux", /\bLinux\b/i],
  ]);
  return `${browser}/${os}`;
}

function parseUserAgentPart(
  userAgent: string,
  fallback: string,
  patterns: [string, RegExp][],
) {
  for (const [name, pattern] of patterns) {
    const match = pattern.exec(userAgent);
    if (match) {
      return `${name}-${match[1]?.replaceAll("_", ".") || "unknown-version"}`;
    }
  }
  return `${fallback}-unknown-version`;
}

export function setReactNativeEnvDetails(
  rnVersion: string,
  os: string,
  osVersion: string | number,
) {
  if (isReactNative) {
    baseEnvStr = `react-native/${rnVersion}/${os}-${osVersion}`;
  }
}

export function getClientEnv() {
  return `js-spark-sdk/${packageVersion} ${baseEnvStr}`;
}
