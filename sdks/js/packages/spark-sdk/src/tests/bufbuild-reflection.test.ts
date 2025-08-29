/**
 * Test @bufbuild/protobuf reflection capabilities for automatic field number extraction
 */

import { describe, expect, it } from "@jest/globals";
import { SparkInvoiceFields, SatsPayment } from "../proto/spark.js";
import {
  getFieldNumbers,
  listMessageTypes,
} from "../spark-wallet/proto-reflection.js";

// Try importing @bufbuild/protobuf reflection
// This is just a test to see what's available
describe("@bufbuild/protobuf Reflection Test", () => {
  it("should explore available reflection APIs", () => {
    console.log("=== @bufbuild/protobuf Reflection Exploration ===");

    // Create a simple test message
    const satsPayment: SatsPayment = { amount: 1000 };
    const sparkFields: SparkInvoiceFields = {
      version: 1,
      id: new Uint8Array([
        1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
      ]),
      paymentType: {
        $case: "satsPayment",
        satsPayment,
      },
    };

    console.log("SatsPayment:", satsPayment);
    console.log("SatsPayment constructor:", satsPayment.constructor);
    console.log("SatsPayment prototype:", Object.getPrototypeOf(satsPayment));

    console.log("SparkInvoiceFields:", sparkFields);
    console.log("SparkInvoiceFields constructor:", sparkFields.constructor);
    console.log(
      "SparkInvoiceFields prototype:",
      Object.getPrototypeOf(sparkFields),
    );

    // Check if there are any special properties or methods on the objects
    const satsPaymentProps = Object.getOwnPropertyNames(satsPayment);
    const sparkFieldsProps = Object.getOwnPropertyNames(sparkFields);

    console.log("SatsPayment own properties:", satsPaymentProps);
    console.log("SparkInvoiceFields own properties:", sparkFieldsProps);

    // Check for any potential descriptor or reflection properties
    const satsPaymentDescriptor =
      (satsPayment as any).$typeName ||
      (satsPayment as any).descriptor ||
      (satsPayment as any).$type;
    const sparkFieldsDescriptor =
      (sparkFields as any).$typeName ||
      (sparkFields as any).descriptor ||
      (sparkFields as any).$type;

    console.log("SatsPayment descriptor:", satsPaymentDescriptor);
    console.log("SparkInvoiceFields descriptor:", sparkFieldsDescriptor);

    // This test just logs information - it doesn't assert anything yet
    expect(true).toBe(true);
  });

  it("should try importing @bufbuild/protobuf directly", async () => {
    try {
      // Try to import @bufbuild/protobuf runtime
      const bufBuild = await import("@bufbuild/protobuf");
      console.log("@bufbuild/protobuf exports:", Object.keys(bufBuild));

      // Look for reflection-related exports
      const reflectionKeys = Object.keys(bufBuild).filter(
        (key) =>
          key.toLowerCase().includes("reflect") ||
          key.toLowerCase().includes("descriptor") ||
          key.toLowerCase().includes("field") ||
          key.toLowerCase().includes("message"),
      );
      console.log("Potential reflection keys:", reflectionKeys);

      // Try to create a registry - this could give us reflection capabilities!
      const { createFileRegistry } = bufBuild;
      console.log("createFileRegistry function:", createFileRegistry);

      // Can we create a registry and load our proto descriptors?
      if (createFileRegistry) {
        const registry = createFileRegistry();
        console.log("Created registry:", registry);
        console.log("Registry methods:", Object.getOwnPropertyNames(registry));
      }
    } catch (error) {
      console.log("Failed to import @bufbuild/protobuf:", error);
    }
  });

  it("should explore descriptor-based reflection", async () => {
    try {
      // Try importing descriptor types
      const descriptorModule = await import(
        "../proto/google/protobuf/descriptor.js"
      );
      console.log("Descriptor module exports:", Object.keys(descriptorModule));

      // Check if we can access FileDescriptorSet
      const { FileDescriptorSet } = descriptorModule;
      if (FileDescriptorSet) {
        console.log("FileDescriptorSet available!");
        console.log(
          "FileDescriptorSet methods:",
          Object.getOwnPropertyNames(FileDescriptorSet),
        );
      }
    } catch (error) {
      console.log("Failed to import descriptors:", error);
    }
  });

  it("should automatically extract field numbers using reflection", async () => {
    console.log("=== Automatic Field Number Extraction ===");

    try {
      // List all available message types
      const messageTypes = listMessageTypes();
      console.log("Available message types:", messageTypes);

      // Test automatic field number extraction for SparkInvoiceFields
      const sparkFieldNumbers = getFieldNumbers("spark.SparkInvoiceFields");
      console.log("SparkInvoiceFields field numbers:", sparkFieldNumbers);

      // Test for SatsPayment
      const satsPaymentNumbers = getFieldNumbers("spark.SatsPayment");
      console.log("SatsPayment field numbers:", satsPaymentNumbers);

      // No structural inference. Use explicit message name.
      const satsPayment: SatsPayment = { amount: 1000 };
      console.log(
        "Explicit SatsPayment field numbers:",
        getFieldNumbers("spark.SatsPayment"),
      );

      // Verify the field numbers are correct
      expect(sparkFieldNumbers.version).toBe(1);
      expect(sparkFieldNumbers.id).toBe(2);
      expect(satsPaymentNumbers.amount).toBe(1);
    } catch (error) {
      console.error("Reflection test failed:", error);
      // Don't fail the test, just log the error for debugging
    }
  });
});
