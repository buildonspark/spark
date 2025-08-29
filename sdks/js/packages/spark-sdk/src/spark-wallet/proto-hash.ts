/**
 * TypeScript implementation of protoreflecthash algorithm for cross-language compatibility.
 * This implements the same objecthash algorithm used by github.com/stackb/protoreflecthash
 * to ensure identical hashes for the same proto messages across Go and JavaScript.
 */

import crypto from "crypto";
import { getFieldNumbers, getFieldMeta } from "./proto-reflection.js";

// ObjectHash type identifiers - must match protoreflecthash constants
const BOOL_IDENTIFIER = "b";
const MAP_IDENTIFIER = "d";
const FLOAT_IDENTIFIER = "f";
const INT_IDENTIFIER = "i";
const LIST_IDENTIFIER = "l";
const BYTE_IDENTIFIER = "r";
const UNICODE_IDENTIFIER = "u";

class SkipFieldError extends Error {
  constructor() {
    super("skip field");
  }
}

function isGoogleProtobufValueNull(value: any): boolean {
  if (value == null) return true;
  if (typeof value === "object" && "$case" in value) {
    const c = (value as any).$case as string | undefined;
    return !c || c === "nullValue";
  }
  return false;
}

const TOP_LEVEL_DISALLOWED = new Set<string>([
  "google.protobuf.Value",
  "google.protobuf.ListValue",
  "google.protobuf.BoolValue",
  "google.protobuf.Int32Value",
  "google.protobuf.Int64Value",
  "google.protobuf.UInt32Value",
  "google.protobuf.UInt64Value",
  "google.protobuf.FloatValue",
  "google.protobuf.DoubleValue",
  "google.protobuf.StringValue",
  "google.protobuf.BytesValue",
]);

interface FieldHashEntry {
  number: number;
  khash: Uint8Array;
  vhash: Uint8Array;
}

/**
 * TypeScript implementation of protoreflecthash for cross-language compatibility
 */
export class ProtoHasher {
  constructor() {}

  async hashProto(message: any, messageTypeName?: string): Promise<Uint8Array> {
    if (message == null) {
      throw new Error("cannot hash nil or invalid message");
    }

    // Disallow hashing of top-level scalar/value wrapper types when type info is provided
    if (messageTypeName && TOP_LEVEL_DISALLOWED.has(messageTypeName)) {
      throw new Error(
        `top-level scalar/value types are not hashable; wrap in a parent message field: ${messageTypeName}`,
      );
    }

    return this.hashMessage(message, messageTypeName);
  }

  private async hashMessage(
    message: any,
    messageTypeName?: string,
  ): Promise<Uint8Array> {
    // Special-case well-known types used in our SDK
    // google.protobuf.Timestamp is represented as JS Date (ts-proto default)
    if (message instanceof Date) {
      const millis = message.getTime();
      const secondsOverride = (message as any).__pbSeconds;
      const nanosOverride = (message as any).__pbNanos;
      const seconds =
        typeof secondsOverride === "number"
          ? Math.floor(secondsOverride)
          : Math.floor(millis / 1000);
      const nanos =
        typeof nanosOverride === "number"
          ? Math.floor(nanosOverride)
          : (millis % 1000) * 1_000_000;

      const secHash = this.hashInt64(seconds);
      const nanoHash = this.hashInt64(nanos);

      const buffer = new Uint8Array(secHash.length + nanoHash.length);
      buffer.set(secHash, 0);
      buffer.set(nanoHash, secHash.length);

      return this.hash(LIST_IDENTIFIER, buffer);
    }

    if (message == null) {
      throw new Error("cannot hash nil message");
    }

    // Get the message fields using provided message type name for descriptor-based field numbers
    const fields = this.getMessageFields(message, messageTypeName);

    const fieldHashes: FieldHashEntry[] = [];

    // Hash each field that is present
    for (const [fieldNumber, fieldInfo] of Object.entries(fields)) {
      if (!fieldInfo?.name) {
        continue;
      }
      const fieldName = fieldInfo.name;
      const fieldType = fieldInfo.type;

      // Resolve value: support oneof extraction carrying a concrete value
      const resolvedValue =
        (fieldInfo as any).value !== undefined
          ? (fieldInfo as any).value
          : message[fieldName];

      // Check if field is present (has a value)
      if (this.isDefault(resolvedValue, fieldType)) {
        continue;
      }

      const fieldValue = resolvedValue;
      const khash = await this.hashFieldKey(parseInt(fieldNumber), fieldName);

      let vhash: Uint8Array | null = null;
      try {
        vhash = await this.hashFieldValue(fieldType, fieldValue);
      } catch (err) {
        if (err instanceof SkipFieldError) {
          // Omit this field entirely
          continue;
        }
        throw err;
      }

      fieldHashes.push({
        number: parseInt(fieldNumber),
        khash,
        vhash,
      });
    }

    // Sort by field number for deterministic ordering
    fieldHashes.sort((a, b) => a.number - b.number);

    // Concatenate all field hashes
    const totalLength = fieldHashes.reduce(
      (sum, fh) => sum + fh.khash.length + fh.vhash.length,
      0,
    );
    const buffer = new Uint8Array(totalLength);
    let offset = 0;

    for (const fh of fieldHashes) {
      buffer.set(fh.khash, offset);
      offset += fh.khash.length;
      buffer.set(fh.vhash, offset);
      offset += fh.vhash.length;
    }

    // Always use map identifier
    const identifier = MAP_IDENTIFIER;

    return this.hash(identifier, buffer);
  }

  private async hashFieldKey(
    fieldNumber: number,
    fieldName: string,
  ): Promise<Uint8Array> {
    return this.hashInt64(fieldNumber);
  }

  private async hashFieldValue(
    fieldType: any,
    value: any,
  ): Promise<Uint8Array> {
    if (Array.isArray(value)) {
      return this.hashList(fieldType.elementType || fieldType, value);
    }

    if (this.isMapType(fieldType)) {
      return this.hashMap(fieldType, value);
    }

    return this.hashValue(fieldType, value);
  }

  private async hashValue(fieldType: any, value: any): Promise<Uint8Array> {
    const typeName = this.getTypeName(fieldType);

    switch (typeName) {
      case "bool":
        return this.hashBool(value);
      case "int32":
      case "int64":
      case "sint32":
      case "sint64":
      case "sfixed32":
      case "sfixed64":
        return this.hashInt64(value);
      case "uint32":
      case "uint64":
      case "fixed32":
      case "fixed64":
        return this.hashUint64(value);
      case "float":
      case "double":
        return this.hashFloat(value);
      case "string":
        return this.hashUnicode(value);
      case "bytes":
        return this.hashBytes(value);
      case "message":
        if (fieldType && typeof fieldType === "object" && fieldType.typeName) {
          return this.hashMessage(value, fieldType.typeName);
        }
        return this.hashMessage(value);
      case "oneof":
        return this.hashOneof(value);
      default:
        // Handle enums as integers
        if (typeof value === "number") {
          return this.hashInt64(value);
        }
        throw new Error(`Unsupported field type: ${typeName}`);
    }
  }

  private async hashOneof(oneofValue: any): Promise<Uint8Array> {
    // For protoc-gen-ts_proto generated types, oneof is represented as:
    // { $case: "fieldName", fieldName: actualValue }
    if (oneofValue && typeof oneofValue === "object" && "$case" in oneofValue) {
      const activeCase = oneofValue.$case;
      const activeValue = oneofValue[activeCase];

      if (activeValue !== undefined) {
        // Hash the active field's value as a message field
        return this.hashMessage(activeValue);
      }
    }

    // If no active case, this oneof is effectively unset and should not be hashed here.
    // Callers shouldn't pass unpopulated oneofs to hashing; treat as invalid input.
    throw new Error("invalid oneof: no active value");
  }

  private async hashList(elementType: any, list: any[]): Promise<Uint8Array> {
    // Empty list is default-equivalent: skip this field entirely
    if (list.length === 0) {
      throw new SkipFieldError();
    }

    const hashes: Uint8Array[] = [];

    // If element type is google.protobuf.Value, disallow null elements
    const isValueList =
      typeof elementType === "object" &&
      (elementType as any)?.typeName === "google.protobuf.Value";

    for (const item of list) {
      if (isValueList && isGoogleProtobufValueNull(item)) {
        throw new Error("cannot hash nil value");
      }
      const itemHash = await this.hashValue(elementType, item);
      hashes.push(itemHash);
    }

    // Concatenate all item hashes
    const totalLength = hashes.reduce((sum, h) => sum + h.length, 0);
    const buffer = new Uint8Array(totalLength);
    let offset = 0;

    for (const h of hashes) {
      buffer.set(h, offset);
      offset += h.length;
    }

    return this.hash(LIST_IDENTIFIER, buffer);
  }

  private async hashMap(
    fieldType: any,
    map: Record<string, any>,
  ): Promise<Uint8Array> {
    const entries: Array<{ khash: Uint8Array; vhash: Uint8Array }> = [];

    const valueIsGoogleValue =
      fieldType?.valueType &&
      fieldType.valueType.typeName === "google.protobuf.Value";

    for (const [key, value] of Object.entries(map)) {
      // Skip entries where the VALUE is google.protobuf.Value set to null
      if (valueIsGoogleValue && isGoogleProtobufValueNull(value)) {
        continue;
      }

      const khash = await this.hashValue(fieldType.keyType, key);

      let vhash: Uint8Array;
      try {
        vhash = await this.hashValue(fieldType.valueType, value);
      } catch (err) {
        // If nested hashing signaled default-equivalence (e.g., empty list), skip this entry
        if (err instanceof SkipFieldError) {
          continue;
        }
        throw err;
      }

      entries.push({ khash, vhash });
    }

    // If, after skipping, no effective entries remain, skip the map field entirely
    if (entries.length === 0) {
      throw new SkipFieldError();
    }

    // Sort by key hash for deterministic ordering
    entries.sort((a, b) => this.compareBytes(a.khash, b.khash));

    // Concatenate all entry hashes
    const totalLength = entries.reduce(
      (sum, e) => sum + e.khash.length + e.vhash.length,
      0,
    );
    const buffer = new Uint8Array(totalLength);
    let offset = 0;

    for (const entry of entries) {
      buffer.set(entry.khash, offset);
      offset += entry.khash.length;
      buffer.set(entry.vhash, offset);
      offset += entry.vhash.length;
    }

    return this.hash(MAP_IDENTIFIER, buffer);
  }

  // Basic hash functions following objecthash spec
  private hashBool(value: boolean): Uint8Array {
    const bytes = value ? new Uint8Array([49]) : new Uint8Array([48]); // "1" or "0"
    return this.hash(BOOL_IDENTIFIER, bytes);
  }

  private hashInt64(value: number | bigint): Uint8Array {
    const b = this.encodeInt64BigEndian(value);
    return this.hash(INT_IDENTIFIER, b);
  }

  private hashUint64(value: number | bigint): Uint8Array {
    const b = this.encodeUint64BigEndian(value);
    return this.hash(INT_IDENTIFIER, b);
  }

  private hashFloat(value: number): Uint8Array {
    // Normalize -0.0 to +0.0
    let f = Object.is(value, -0) ? 0 : value;
    // Use a canonical NaN representation by forcing to NaN when NaN
    if (Number.isNaN(f)) {
      f = Number.NaN;
    }

    const buf = new ArrayBuffer(8);
    const view = new DataView(buf);
    view.setFloat64(0, f, false); // big-endian
    return this.hash(FLOAT_IDENTIFIER, new Uint8Array(buf));
  }

  private hashUnicode(value: string): Uint8Array {
    return this.hash(UNICODE_IDENTIFIER, new TextEncoder().encode(value));
  }

  private hashBytes(value: Uint8Array | ArrayBuffer | number[]): Uint8Array {
    let bytes: Uint8Array;
    if (value instanceof Uint8Array) {
      bytes = value;
    } else if (value instanceof ArrayBuffer) {
      bytes = new Uint8Array(value);
    } else {
      bytes = new Uint8Array(value);
    }
    return this.hash(BYTE_IDENTIFIER, bytes);
  }

  // Note: No NIL hashing in JS. Nil/undefined should never be hashed and will error.

  private hash(typeIdentifier: string, data: Uint8Array): Uint8Array {
    const hasher = crypto.createHash("sha256");
    hasher.update(typeIdentifier, "utf8");
    hasher.update(data);
    return new Uint8Array(hasher.digest());
  }

  // Generic protobuf field introspection using real protobuf field numbers
  private getMessageFields(
    message: any,
    messageTypeName?: string,
  ): Record<number, any> {
    const fields: Record<number, any> = {};

    // Use descriptor-based numbers when a message type name is provided.
    const reflectionNumbers = messageTypeName
      ? getFieldNumbers(messageTypeName)
      : {};
    const fieldMeta = messageTypeName ? getFieldMeta(messageTypeName) : {};

    function camelToSnake(name: string): string {
      return name
        .replace(/([A-Z])/g, "_$1")
        .replace(/^_/, "")
        .toLowerCase();
    }

    // Track used field numbers to avoid conflicts
    const usedTags = new Set<number>();

    // Process all enumerable properties
    const allKeys = Object.getOwnPropertyNames(message).concat(
      Object.keys(message),
    );
    const uniqueKeys = [...new Set(allKeys)];

    for (const fieldName of uniqueKeys) {
      const value = message[fieldName];

      // Handle oneof fields specially
      if (this.isOneofField(value)) {
        const oneofInfo = this.extractOneofField(
          fieldName,
          value,
          reflectionNumbers as any,
        );
        if (oneofInfo) {
          const { actualFieldNumber, actualFieldName, actualValue } = oneofInfo;

          usedTags.add(actualFieldNumber);

          const snakeCase = camelToSnake(actualFieldName);
          const nestedTypeName = (fieldMeta as any)[snakeCase]?.typeName as
            | string
            | undefined;
          const inferred = this.inferFieldType(actualValue);
          const typeWithHint = nestedTypeName
            ? { type: "message", typeName: nestedTypeName }
            : inferred;
          fields[actualFieldNumber] = {
            name: actualFieldName,
            type: typeWithHint,
            value: actualValue,
          };
        }
        continue;
      }

      // Get the canonical protobuf field number for this field using reflection only
      const snake = camelToSnake(fieldName);
      const canonicalTag =
        reflectionNumbers && snake in (reflectionNumbers as any)
          ? ((reflectionNumbers as any)[snake] as number)
          : undefined;
      if (canonicalTag === undefined) {
        throw new Error(
          `Unknown field '${fieldName}' for message type '${messageTypeName ?? "<unknown>"}'`,
        );
      }
      const fieldNumber = canonicalTag;

      usedTags.add(fieldNumber);

      if (value !== undefined) {
        const snakeCase = camelToSnake(fieldName);
        const nestedTypeName = (fieldMeta as any)[snakeCase]?.typeName as
          | string
          | undefined;
        const inferred = this.inferFieldType(value);
        const typeWithHint = nestedTypeName
          ? { type: "message", typeName: nestedTypeName }
          : inferred;
        fields[fieldNumber] = {
          name: fieldName,
          type: typeWithHint,
          value,
        };
      }
    }

    if (Object.keys(fields).length === 0) {
      const dbgReflectionKeys = Object.keys(reflectionNumbers || {});
      const dbgAllKeys = Object.getOwnPropertyNames(message).concat(
        Object.keys(message),
      );
      console.log("proto-hash: no fields found", {
        messageTypeName,
        reflectionKeys: dbgReflectionKeys,
        messageKeys: Array.from(new Set(dbgAllKeys)),
      });
      throw new Error(
        "No fields found in message (missing or invalid messageTypeName)",
      );
    }

    return fields;
  }

  private isOneofField(value: any): boolean {
    return (
      value &&
      typeof value === "object" &&
      "$case" in value &&
      typeof (value as any).$case === "string"
    );
  }

  private extractOneofField(
    fieldName: string,
    value: any,
    reflectionNumbers: Record<string, number>,
  ): {
    actualFieldNumber: number;
    actualFieldName: string;
    actualValue: any;
  } | null {
    if (!value || !("$case" in value)) return null;

    const actualFieldName = (value as any).$case as string;
    const actualValue = (value as any)[actualFieldName];
    const snake = actualFieldName
      .replace(/([A-Z])/g, "_$1")
      .replace(/^_/, "")
      .toLowerCase();
    const actualFieldNumber = (reflectionNumbers as any)[snake] as
      | number
      | undefined;
    if (!actualFieldNumber) return null;

    return {
      actualFieldNumber,
      actualFieldName,
      actualValue,
    };
  }

  private inferFieldType(value: any): string {
    if (typeof value === "boolean") return "bool";
    if (typeof value === "number") {
      if (Number.isInteger(value) && value >= 0) {
        if (value <= 0xffffffff) {
          return "uint32";
        } else {
          return "uint64";
        }
      }
      return "int64";
    }
    if (typeof value === "bigint") return "uint64";
    if (typeof value === "string") return "string";
    if (value instanceof Uint8Array) return "bytes";
    if (Array.isArray(value)) return "list";
    if (typeof value === "object" && value !== null) return "message";
    return "unknown";
  }

  private isDefault(value: any, fieldType: any): boolean {
    if (value == null) {
      return true;
    }

    if (
      this.getTypeName(fieldType) === "message" &&
      (fieldType as any).typeName === "google.protobuf.Value"
    ) {
      if (isGoogleProtobufValueNull(value)) return true;
    }

    if (Array.isArray(value)) {
      return value.length === 0;
    }

    if (this.isMapType(fieldType)) {
      return Object.keys(value).length === 0;
    }

    switch (this.getTypeName(fieldType)) {
      case "bool":
        return value === false;
      case "int32":
      case "int64":
      case "sint32":
      case "sint64":
      case "sfixed32":
      case "sfixed64":
      case "uint32":
      case "uint64":
      case "fixed32":
      case "fixed64":
        return value === 0 || value === 0n;
      case "float":
      case "double":
        return value === 0;
      case "string":
        return value.length === 0;
      case "bytes":
        return value.length === 0;
      default:
        if (typeof value === "number") {
          return value === 0;
        }
    }
    return false;
  }

  private getTypeName(fieldType: any): string {
    if (typeof fieldType === "string") {
      return fieldType;
    }
    if (fieldType.type) {
      return fieldType.type;
    }
    if ((fieldType as any).resolvedType) {
      return "message";
    }
    return "unknown";
  }

  private getMessageFullName(message: any): string {
    const constructorName = message?.constructor?.name;
    if (constructorName && constructorName !== "Object") {
      return constructorName;
    }
    return "protobuf.Message";
  }

  private isMapType(fieldType: any): boolean {
    return fieldType.map === true || (fieldType.keyType && fieldType.valueType);
  }

  private isProto3(message: any): boolean {
    return true; // Assume proto3 for now
  }

  private compareBytes(a: Uint8Array, b: Uint8Array): number {
    const minLength = Math.min(a.length, b.length);
    for (let i = 0; i < minLength; i++) {
      const aVal = a[i];
      const bVal = b[i];
      if (aVal !== undefined && bVal !== undefined && aVal !== bVal) {
        return aVal - bVal;
      }
    }
    return a.length - b.length;
  }

  private floatNormalize(originalFloat: number): string {
    if (originalFloat === 0) {
      return "+0:";
    }
    let f = originalFloat;
    let s = "+";
    if (f < 0) {
      s = "-";
      f = -f;
    }
    let e = 0;
    while (f > 1) {
      f /= 2;
      e++;
    }
    while (f <= 0.5) {
      f *= 2;
      e--;
    }
    s += `${e}:`;
    if (f > 1 || f <= 0.5) {
      throw new Error(`Could not normalize float: ${originalFloat}`);
    }
    while (f !== 0) {
      if (f >= 1) {
        s += "1";
        f--;
      } else {
        s += "0";
      }
      if (f >= 1) {
        throw new Error(`Could not normalize float: ${originalFloat}`);
      }
      if (s.length >= 1000) {
        throw new Error(`Could not normalize float: ${originalFloat}`);
      }
      f *= 2;
    }
    return s;
  }

  private encodeUint64BigEndian(value: number | bigint): Uint8Array {
    let v = typeof value === "bigint" ? value : BigInt(value);
    const out = new Uint8Array(8);
    for (let i = 7; i >= 0; i--) {
      out[i] = Number(v & 0xffn);
      v >>= 8n;
    }
    return out;
  }

  private encodeInt64BigEndian(value: number | bigint): Uint8Array {
    let v = typeof value === "bigint" ? value : BigInt(value);
    const mask = (1n << 64n) - 1n;
    if (v < 0) {
      v = v & mask;
    }
    const out = new Uint8Array(8);
    for (let i = 7; i >= 0; i--) {
      out[i] = Number(v & 0xffn);
      v >>= 8n;
    }
    return out;
  }
}

/**
 * Create a new ProtoHasher instance
 */
export function createProtoHasher(): ProtoHasher {
  return new ProtoHasher();
}

/**
 * Hash a protobuf message with default options
 */
export async function hashProto(message: any): Promise<Uint8Array> {
  const hasher = new ProtoHasher();
  return hasher.hashProto(message);
}
