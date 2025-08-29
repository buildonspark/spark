#!/usr/bin/env node

/**
 * Script to embed protobuf descriptors as a JavaScript module
 * Converts the binary descriptor file to a base64-encoded string that can be imported
 */

import { readFileSync, writeFileSync } from "fs";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

const descriptorPath = join(__dirname, "..", "src", "spark_descriptors.pb");
const outputPath = join(
  __dirname,
  "..",
  "src",
  "spark-wallet",
  "proto-descriptors.ts",
);

try {
  console.log("Embedding protobuf descriptors...");

  // Read the binary descriptor file
  const descriptorBytes = readFileSync(descriptorPath);

  // Convert to base64 for embedding
  const base64Data = descriptorBytes.toString("base64");

  // Generate TypeScript module
  const tsContent = `/**
 * Auto-generated protobuf descriptors
 * Generated from spark_descriptors.pb
 * 
 * This file contains the binary protobuf descriptors encoded as base64
 * for runtime field number extraction and reflection.
 */

// Base64-encoded FileDescriptorSet
export const SPARK_DESCRIPTORS_BASE64 = "${base64Data}";

// Convert back to Uint8Array when needed
export function getSparkDescriptorBytes(): Uint8Array {
  // Convert base64 to binary
  const binaryString = atob(SPARK_DESCRIPTORS_BASE64);
  const bytes = new Uint8Array(binaryString.length);
  for (let i = 0; i < binaryString.length; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes;
}
`;

  // Write the TypeScript module
  writeFileSync(outputPath, tsContent, "utf8");

  console.log(`✅ Descriptors embedded successfully!`);
  console.log(`   Binary size: ${descriptorBytes.length} bytes`);
  console.log(`   Base64 size: ${base64Data.length} characters`);
  console.log(`   Output: ${outputPath}`);
} catch (error) {
  console.error("❌ Failed to embed descriptors:", error);
  process.exit(1);
}
