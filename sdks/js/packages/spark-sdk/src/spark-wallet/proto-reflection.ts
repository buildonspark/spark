/**
 * Automatic field number extraction using @bufbuild/protobuf reflection
 * This replaces manual field number mapping with runtime descriptor introspection
 */

import { FileDescriptorSet } from "../proto/google/protobuf/descriptor.js";
import { getSparkDescriptorBytes } from "./proto-descriptors.js";

// Cache for the registry to avoid reloading descriptors
let _registry: any = null;

/**
 * Helper function to process nested messages recursively
 */
function processNestedMessages(
  messageDescriptor: any,
  parentFullName: string,
  messageMap: Map<string, any>,
) {
  if (messageDescriptor.nestedType) {
    for (const nestedMessage of messageDescriptor.nestedType) {
      const nestedFullName = `${parentFullName}.${nestedMessage.name}`;
      messageMap.set(nestedFullName, nestedMessage);

      // Recursively process nested messages
      processNestedMessages(nestedMessage, nestedFullName, messageMap);
    }
  }
}

/**
 * Get or create the protobuf registry with our descriptors loaded
 */
function getRegistry() {
  if (_registry) {
    return _registry;
  }

  try {
    // Load the embedded descriptors
    console.log("Loading embedded protobuf descriptors...");
    const descriptorBytes = getSparkDescriptorBytes();

    // Decode the FileDescriptorSet
    const descriptorSet = FileDescriptorSet.decode(descriptorBytes);

    // Instead of using the problematic registry.addFile(), we'll work directly
    // with the decoded FileDescriptorSet data
    _registry = {
      descriptorSet,
      fileMap: new Map(),
      messageMap: new Map(),
    };

    // Build lookup maps from the descriptor set
    for (const fileDescriptor of descriptorSet.file) {
      console.log(`Processing proto file: ${fileDescriptor.name}`);
      _registry.fileMap.set(fileDescriptor.name, fileDescriptor);

      // Process messages in this file
      if (fileDescriptor.messageType) {
        for (const messageDescriptor of fileDescriptor.messageType) {
          const pkg = fileDescriptor.package ?? "";
          const fullName =
            pkg.length > 0
              ? `${pkg}.${messageDescriptor.name}`
              : String(messageDescriptor.name);
          _registry.messageMap.set(fullName, messageDescriptor);

          // Process nested messages
          processNestedMessages(
            messageDescriptor,
            fullName,
            _registry.messageMap,
          );
        }
      }
    }

    console.log(
      `Registry loaded with ${descriptorSet.file.length} proto files`,
    );
    console.log(`Found ${_registry.messageMap.size} message types`);
    return _registry;
  } catch (error) {
    console.error("Failed to load protobuf descriptors:", error);
    throw error;
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
      console.warn(`Message type not found: ${messageTypeName}`);
      console.log(
        "Available message types:",
        Array.from(registry.messageMap.keys()),
      );
      return {};
    }

    const fieldNumbers: Record<string, number> = {};

    // Extract field numbers from the descriptor
    if (messageDescriptor.field) {
      for (const field of messageDescriptor.field) {
        fieldNumbers[field.name] = field.number;
      }
    }

    console.log(`Field numbers for ${messageTypeName}:`, fieldNumbers);
    return fieldNumbers;
  } catch (error) {
    console.error(`Failed to get field numbers for ${messageTypeName}:`, error);
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
    const types = Array.from(registry.messageMap.keys()) as string[];

    return types.sort();
  } catch (error) {
    console.error("Failed to list message types:", error);
    return [];
  }
}

/**
 * Return per-field metadata for a message type.
 * - Keys are snake_case field names as present in the proto descriptor
 * - Values include field number, oneof index if applicable, and nested type name for message fields
 */
export function getFieldMeta(
  messageTypeName: string,
): Record<string, { number: number; oneofIndex?: number; typeName?: string }> {
  try {
    const registry = getRegistry();
    const descriptor = registry.messageMap.get(messageTypeName);
    if (!descriptor) {
      return {};
    }
    const meta: Record<
      string,
      { number: number; oneofIndex?: number; typeName?: string }
    > = {};
    const fields = descriptor.field || [];
    for (const f of fields) {
      const entry: { number: number; oneofIndex?: number; typeName?: string } =
        {
          number: f.number,
        };
      if (typeof f.oneofIndex === "number") {
        entry.oneofIndex = f.oneofIndex;
      }
      // If this is a message-typed field, record fully qualified nested type name
      // f.typeName may be like ".spark.TokensPayment"; normalize by trimming leading dot
      const TYPE_MESSAGE = 11; // google.protobuf.FieldDescriptorProto.Type.TYPE_MESSAGE
      if (
        f.type === TYPE_MESSAGE &&
        typeof f.typeName === "string" &&
        f.typeName.length > 0
      ) {
        entry.typeName = f.typeName.startsWith(".")
          ? f.typeName.slice(1)
          : f.typeName;
      }
      meta[f.name] = entry;
    }
    return meta;
  } catch {
    return {};
  }
}
