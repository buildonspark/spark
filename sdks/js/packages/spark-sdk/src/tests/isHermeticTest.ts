import { isNode } from "@lightsparkdev/core";
import fs from "fs";

/**
 * Checks if the current environment is a hermetic test environment.
 * A hermetic test environment is identified by either:
 * 1. The existence of a marker file at /tmp/spark_hermetic (Node.js only)
 * 2. The HERMETIC_TEST environment variable being set to "true"
 *
 * @returns {boolean} True if running in a hermetic test environment, false otherwise
 */
export function isHermeticTest() {
  if (isNode) {
    return (
      (fs?.existsSync?.("/tmp/spark_hermetic") ?? false) ||
      process.env.HERMETIC_TEST === "true"
    );
  }

  return (
    (typeof process !== "undefined" && process.env?.HERMETIC_TEST === "true") ||
    false
  );
}
