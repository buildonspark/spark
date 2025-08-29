import { readFileSync } from "fs";
import { join } from "path";
import {
  SparkInvoiceFields,
  SatsPayment,
  TokensPayment,
} from "../dist/proto/spark.js";
import { createProtoHasher } from "../dist/spark-wallet/proto-hash.js";

// Load shared test data
const testDataPath = join(
  process.cwd(),
  "../../../testdata/cross_language_hash_cases.json",
);
const testData = JSON.parse(readFileSync(testDataPath, "utf8"));

// Helper to convert test data to TypeScript protobuf objects
function createSparkInvoiceFieldsFromTestData(testCase) {
  const data = testCase.spark_invoice_fields;

  const sparkInvoiceFields = {
    version: data.version,
    id: new Uint8Array(data.id),
    paymentType: createPaymentType(data.payment_type),
  };

  // Add optional fields if present
  if (data.memo !== undefined) {
    sparkInvoiceFields.memo = data.memo;
  }

  if (data.sender_public_key !== undefined) {
    sparkInvoiceFields.senderPublicKey = new Uint8Array(data.sender_public_key);
  }

  if (data.expiry_time !== undefined) {
    sparkInvoiceFields.expiryTime = new Date(data.expiry_time.seconds * 1000);
  }

  return sparkInvoiceFields;
}

function createPaymentType(paymentTypeData) {
  if (paymentTypeData.type === "sats_payment") {
    return {
      $case: "satsPayment",
      satsPayment: {
        amount: paymentTypeData.sats_payment.amount,
      },
    };
  } else if (paymentTypeData.type === "tokens_payment") {
    return {
      $case: "tokensPayment",
      tokensPayment: {
        tokenIdentifier: new Uint8Array(
          paymentTypeData.tokens_payment.token_identifier,
        ),
        amount: new Uint8Array(paymentTypeData.tokens_payment.amount),
      },
    };
  }

  throw new Error(`Unknown payment type: ${paymentTypeData.type}`);
}

function toHexString(bytes) {
  return Array.from(bytes)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

async function generateAllHashes() {
  const hasher = createProtoHasher();

  console.log("=== Generated Hashes for cross_language_hash_cases.json ===\n");

  for (const testCase of testData.test_cases) {
    try {
      const sparkInvoiceFields = createSparkInvoiceFieldsFromTestData(testCase);
      const hash = await hasher.hashProto(sparkInvoiceFields);
      const hexHash = toHexString(hash);

      console.log(`${testCase.name}: ${hexHash}`);
    } catch (error) {
      console.log(`${testCase.name}: ERROR - ${error.message}`);
    }
  }
}

generateAllHashes().catch(console.error);
