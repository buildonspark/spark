import fs from "fs";
import path from "path";
import url from "url";

const here = url.fileURLToPath(import.meta.url);
const pkgRoot = path.resolve(here, "..", "..", "..");
const repoRoot = path.resolve(pkgRoot, "..", "..", "..");

const src = path.join(
  repoRoot,
  "spark",
  "testdata",
  "cross_language_hash_cases.json",
);
const dst = path.join(
  repoRoot,
  "spark",
  "testdata",
  "cross_language_hash_cases_proto.json",
);

function b64(bytes) {
  return Buffer.from(bytes).toString("base64");
}

function camelCase(s) {
  return s.replace(/_([a-z])/g, (_, c) => c.toUpperCase());
}

function isNumericByteArray(arr) {
  if (!Array.isArray(arr)) return false;
  if (arr.length === 0) return true; // treat empty arrays as bytes → empty base64
  for (const v of arr) {
    if (typeof v !== "number" || v < 0 || v > 255 || !Number.isInteger(v)) {
      return false;
    }
  }
  return true;
}

function isOneofWrapper(obj) {
  if (
    !obj ||
    typeof obj !== "object" ||
    Array.isArray(obj) ||
    typeof obj.type !== "string"
  ) {
    return false;
  }
  return Object.prototype.hasOwnProperty.call(obj, obj.type);
}

function transformValue(v) {
  if (Array.isArray(v)) {
    // Treat arrays of uint8 as bytes → base64
    if (isNumericByteArray(v)) {
      return b64(v);
    }
    return v.map(transformValue);
  }
  if (v && typeof v === "object") {
    // Timestamp-like object {seconds, nanos?} → RFC3339 string
    const keys = Object.keys(v);
    if (
      (keys.length === 1 || keys.length === 2) &&
      typeof v.seconds === "number" &&
      (v.nanos === undefined || typeof v.nanos === "number")
    ) {
      return formatRfc3339NoNanos(v.seconds);
    }
    const out = {};
    for (const [k, val] of Object.entries(v)) {
      // If a value is a oneof wrapper, flatten it.
      if (isOneofWrapper(val)) {
        const type = val.type;
        const oneofValue = val[type];
        if (oneofValue !== undefined) {
          out[camelCase(type)] = transformValue(oneofValue);
        }
      } else {
        out[camelCase(k)] = transformValue(val);
      }
    }
    return out;
  }
  return v;
}

function convertOne(tc) {
  // 1) Transform keys to camelCase, bytes arrays → base64, timestamps → RFC3339
  let m = transformValue(tc.spark_invoice_fields || {});
  return {
    name: tc.name,
    description: tc.description,
    expectedHash: tc.expected_hash,
    sparkInvoiceFields: m,
  };
}

const raw = JSON.parse(fs.readFileSync(src, "utf8"));
const out = {
  description:
    "Cross-language hash cases in canonical Protobuf JSON for spark.SparkInvoiceFields",
  testCases: (raw.test_cases || [])
    .filter((tc) => tc.name !== "with_expiry_time_nanos")
    .map(convertOne),
};

fs.writeFileSync(dst, JSON.stringify(out, null, 2) + "\n", "utf8");
console.log(`Wrote ${dst}`);

function formatRfc3339NoNanos(seconds) {
  // Build the base date-time from whole seconds in UTC
  const d = new Date(seconds * 1000);
  const yyyy = d.getUTCFullYear();
  const mm = String(d.getUTCMonth() + 1).padStart(2, "0");
  const dd = String(d.getUTCDate()).padStart(2, "0");
  const hh = String(d.getUTCHours()).padStart(2, "0");
  const mi = String(d.getUTCMinutes()).padStart(2, "0");
  const ss = String(d.getUTCSeconds()).padStart(2, "0");
  return `${yyyy}-${mm}-${dd}T${hh}:${mi}:${ss}Z`;
}
