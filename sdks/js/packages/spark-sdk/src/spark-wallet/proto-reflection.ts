/**
 * Automatic field number extraction using @bufbuild/protobuf reflection
 * This replaces manual field number mapping with runtime descriptor introspection
 */

import {
  type DescriptorProto,
  type FieldDescriptorProto,
  FieldDescriptorProto_Label,
  FieldDescriptorProto_Type,
  FileDescriptorSet,
  type FileDescriptorProto,
} from "../proto/google/protobuf/descriptor.js";
import { getSparkDescriptorBytes } from "./proto-descriptors.js";

type ProtoRegistry = {
  descriptorSet: ReturnType<typeof FileDescriptorSet.decode>;
  fileMap: Map<string, FileDescriptorProto>;
  messageMap: Map<string, DescriptorProto>;
};

type FieldMeta = {
  number: number;
  oneofIndex?: number;
  typeName?: string;
  repeated?: boolean;
  type?: number;
};

let registryCache: ProtoRegistry | null = null;

function formatErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Helper function to process nested messages recursively
 */
function processNestedMessages(
  messageDescriptor: DescriptorProto,
  parentFullName: string,
  messageMap: Map<string, DescriptorProto>,
) {
  if (messageDescriptor.nestedType.length > 0) {
    for (const nestedMessage of messageDescriptor.nestedType) {
      if (!nestedMessage.name) {
        continue;
      }
      const nestedFullName = `${parentFullName}.${nestedMessage.name}`;
      messageMap.set(nestedFullName, nestedMessage);

      processNestedMessages(nestedMessage, nestedFullName, messageMap);
    }
  }
}

/**
 * Get or create the protobuf registry with our descriptors loaded
 */
function getRegistry(): ProtoRegistry {
  if (registryCache) {
    return registryCache;
  }

  try {
    // Load the embedded descriptors
    const descriptorBytes = getSparkDescriptorBytes();

    // Decode the FileDescriptorSet
    const descriptorSet = FileDescriptorSet.decode(descriptorBytes);

    // Instead of using the problematic registry.addFile(), we'll work directly
    // with the decoded FileDescriptorSet data
    const registry: ProtoRegistry = {
      descriptorSet,
      fileMap: new Map(),
      messageMap: new Map(),
    };

    // Build lookup maps from the descriptor set
    for (const fileDescriptor of descriptorSet.file) {
      if (fileDescriptor.name) {
        registry.fileMap.set(fileDescriptor.name, fileDescriptor);
      }

      // Process messages in this file
      if (fileDescriptor.messageType.length > 0) {
        for (const messageDescriptor of fileDescriptor.messageType) {
          if (!messageDescriptor.name) {
            continue;
          }
          const pkg = fileDescriptor.package ?? "";
          const fullName =
            pkg.length > 0
              ? `${pkg}.${messageDescriptor.name}`
              : String(messageDescriptor.name);
          registry.messageMap.set(fullName, messageDescriptor);

          processNestedMessages(
            messageDescriptor,
            fullName,
            registry.messageMap,
          );
        }
      }
    }

    registryCache = registry;
    return registry;
  } catch (error) {
    throw new Error(
      `Failed to load protobuf descriptors: ${formatErrorMessage(error)}`,
      { cause: error },
    );
  }
}

/**
 * Get field numbers for a specific message type
 * @param messageTypeName - Full message type name (e.g. "spark.SparkInvoiceFields")
 * @returns Record of field names to field numbers
 */
export function getFieldNumbers(
  messageTypeName: string,
): Record<string, number> {
  try {
    const registry = getRegistry();

    // Get the message descriptor from our custom registry
    const messageDescriptor = registry.messageMap.get(messageTypeName);

    if (!messageDescriptor) {
      return {};
    }

    const fieldNumbers: Record<string, number> = {};

    // Extract field numbers from the descriptor
    if (messageDescriptor.field.length > 0) {
      for (const field of messageDescriptor.field) {
        if (field.name && field.number != null) {
          fieldNumbers[field.name] = field.number;
        }
      }
    }

    return fieldNumbers;
  } catch {
    return {};
  }
}

/**
 * List all available message types in the registry
 */
export function listMessageTypes(): string[] {
  try {
    const registry = getRegistry();

    // Get all message type names from our custom registry
    const types = Array.from(registry.messageMap.keys());

    return types.sort();
  } catch {
    return [];
  }
}

/**
 * Return per-field metadata for a message type.
 * - Keys are snake_case field names as present in the proto descriptor
 * - Values include field number, oneof index if applicable, nested type name for message fields,
 *   whether the field is repeated, and the element type
 */
export function getFieldMeta(
  messageTypeName: string,
): Record<string, FieldMeta> {
  try {
    const registry = getRegistry();
    const descriptor = registry.messageMap.get(messageTypeName);
    if (!descriptor) {
      return {};
    }
    const meta: Record<string, FieldMeta> = {};
    const fields = descriptor.field || [];

    for (const f of fields) {
      const entry = makeFieldMeta(f);
      if (!entry || !f.name) {
        continue;
      }
      meta[f.name] = entry;
    }
    return meta;
  } catch {
    return {};
  }
}

function makeFieldMeta(f: FieldDescriptorProto): FieldMeta | null {
  if (f.number == null) {
    return null;
  }
  const entry: FieldMeta = {
    number: f.number,
  };
  if (typeof f.oneofIndex === "number") {
    entry.oneofIndex = f.oneofIndex;
  }
  if (f.label === FieldDescriptorProto_Label.LABEL_REPEATED) {
    entry.repeated = true;
  }
  if (typeof f.type === "number") {
    entry.type = f.type;
  }
  if (
    f.type === FieldDescriptorProto_Type.TYPE_MESSAGE &&
    typeof f.typeName === "string" &&
    f.typeName.length > 0
  ) {
    entry.typeName = f.typeName.startsWith(".")
      ? f.typeName.slice(1)
      : f.typeName;
  }
  return entry;
}
