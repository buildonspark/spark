#!/usr/bin/env node

"use strict";

const fs = require("fs");
const path = require("path");

const PREBUILDS_DIR = path.resolve(__dirname, "..", "prebuilds");
const ADDON_FILENAME = "buildonspark__spark-frost-bare-addon.bare";
const PE_HEADER_POINTER_OFFSET = 0x3c;
const PE_SIGNATURE = "PE\0\0";
const COFF_TIMESTAMP_OFFSET_FROM_PE_HEADER = 8;

function normalizeTimestamp(filePath) {
  const buffer = fs.readFileSync(filePath);

  if (buffer.length < PE_HEADER_POINTER_OFFSET + 4) {
    throw new Error(`${filePath} is too small to contain a PE header pointer`);
  }

  const peHeaderOffset = buffer.readUInt32LE(PE_HEADER_POINTER_OFFSET);
  const timestampOffset = peHeaderOffset + COFF_TIMESTAMP_OFFSET_FROM_PE_HEADER;

  if (buffer.length < timestampOffset + 4) {
    throw new Error(`${filePath} is too small to contain a PE COFF header`);
  }

  if (
    buffer.toString("latin1", peHeaderOffset, peHeaderOffset + 4) !==
    PE_SIGNATURE
  ) {
    throw new Error(`${filePath} does not contain a PE signature`);
  }

  const timestamp = buffer.readUInt32LE(timestampOffset);
  if (timestamp === 0) {
    return false;
  }

  buffer.writeUInt32LE(0, timestampOffset);
  fs.writeFileSync(filePath, buffer);
  return true;
}

function findWin32Prebuilds() {
  if (!fs.existsSync(PREBUILDS_DIR)) {
    return [];
  }

  return fs
    .readdirSync(PREBUILDS_DIR, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith("win32-"))
    .map((entry) => path.join(PREBUILDS_DIR, entry.name, ADDON_FILENAME))
    .filter((filePath) => fs.existsSync(filePath));
}

const normalized = [];

for (const filePath of findWin32Prebuilds()) {
  if (normalizeTimestamp(filePath)) {
    normalized.push(path.relative(process.cwd(), filePath));
  }
}

if (normalized.length === 0) {
  console.log("No win32 prebuild PE timestamps needed normalization.");
} else {
  console.log("Normalized win32 prebuild PE timestamps:");
  for (const filePath of normalized) {
    console.log(`- ${filePath}`);
  }
}
