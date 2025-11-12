import { isNode, isBare } from "@lightsparkdev/core";
import { UAParser } from "ua-parser-js";

export const isReactNative =
  "navigator" in globalThis && navigator.product === "ReactNative";

export const isBun = "Bun" in globalThis;

export const isWebExtension =
  /* globalThis.chrome actually exists in extension contexts for all browsers for legacy reasons: */
  "chrome" in globalThis && globalThis.chrome.runtime?.id;
export const isWebExtensionContentScript =
  isWebExtension && "window" in globalThis;
export const isWebExtensionBackgroundScript =
  isWebExtension && !isWebExtensionContentScript;

/* navigator.userAgent exists in browsers and extension contexts: */
const userAgent =
  "navigator" in globalThis
    ? parseUserAgent(globalThis.navigator.userAgent) || "unknown-user-agent"
    : undefined;

declare const __PACKAGE_VERSION__: string;

export const packageVersion =
  typeof __PACKAGE_VERSION__ !== "undefined" ? __PACKAGE_VERSION__ : "unknown";

let baseEnvStr = "unknown";
if (isBun) {
  const bunVersion =
    "version" in globalThis.Bun ? globalThis.Bun.version : "unknown-version";
  baseEnvStr = `bun/${bunVersion}`;
} else if (isNode) {
  baseEnvStr = `node/${globalThis.process.version}`;
} else if (isReactNative) {
  baseEnvStr = "react-native";
} else if (isBare) {
  type BareType = {
    version: string;
  };
  const bareVersion = (Bare as BareType).version;
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

function parseUserAgent(userAgent: string) {
  const parser = UAParser(userAgent);
  const browserName = parser.browser.name
    ? parser.browser.name.toLowerCase().replaceAll(" ", "-")
    : "unknown-browser";
  const browserVersion = parser.browser.version
    ? parser.browser.version
    : "unknown-version";
  const osName = parser.os.name
    ? parser.os.name.toLowerCase().replaceAll(" ", "-")
    : "unknown-os";
  const osVersion = parser.os.version ? parser.os.version : "unknown-version";
  return `${browserName}-${browserVersion}/${osName}-${osVersion}`;
}

export const clientEnv = `js-spark-sdk/${packageVersion} ${baseEnvStr}`;
