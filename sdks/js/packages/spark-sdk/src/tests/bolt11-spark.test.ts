import { describe, expect, it } from "@jest/globals";
import { bech32 } from "@scure/base";
import { decodeInvoice } from "../services/bolt11-spark.js";

const SIGNATURE_WORD_COUNT = (65 * 8) / 5;

/** Rewrites one bech32 word and recomputes the checksum, as an attacker would. */
function tamperWithInvoice(invoice: string, wordIndex: number): string {
  const humanReadablePart = invoice.slice(0, invoice.lastIndexOf("1"));
  const { words } = bech32.decode(
    invoice as `${string}1${string}`,
    Number.MAX_SAFE_INTEGER,
  );
  const tampered = Array.from(words);
  tampered[wordIndex] = (tampered[wordIndex]! + 1) % 32;
  return bech32.encode(
    humanReadablePart as "lnbc",
    tampered,
    Number.MAX_SAFE_INTEGER,
  );
}

function corruptInvoiceSignature(invoice: string): string {
  const humanReadablePart = invoice.slice(0, invoice.lastIndexOf("1"));
  const { words } = bech32.decode(
    invoice as `${string}1${string}`,
    Number.MAX_SAFE_INTEGER,
  );
  const corrupted = Array.from(words);
  corrupted.fill(0, -SIGNATURE_WORD_COUNT);
  return bech32.encode(
    humanReadablePart as "lnbc",
    corrupted,
    Number.MAX_SAFE_INTEGER,
  );
}

// examples taken from BOLT-11 spec § Examples (2025-06-04)
// https://github.com/lightning/bolts/blob/master/11-payment-encoding.md#examples
describe("spark bolt11 invoice decoding", () => {
  it("decodes bolt11 example-1", () => {
    const invoice =
      "lnbc1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6twvus8g6rfwvs8qun0dfjkxaq9qrsgq357wnc5r2ueh7ck6q93dj32dlqnls087fxdwk8qakdyafkq3yap9us6v52vjjsrvywa6rt52cm9r9zqt8r2t7mlcwspyetp5h2tztugp9lfyql";
    const { amountMSats, fallbackAddress, paymentHash } =
      decodeInvoice(invoice);
    expect(amountMSats).toBe(null);
    expect(fallbackAddress).toBe(undefined);
    expect(paymentHash).toBe(
      "0001020304050607080900010203040506070809000102030405060708090102",
    );
  });

  it("decodes bolt11 example-2", () => {
    const invoice =
      "lnbc2500u1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpu9qrsgquk0rl77nj30yxdy8j9vdx85fkpmdla2087ne0xh8nhedh8w27kyke0lp53ut353s06fv3qfegext0eh0ymjpf39tuven09sam30g4vgpfna3rh";
    const { amountMSats, fallbackAddress, paymentHash } =
      decodeInvoice(invoice);
    expect(amountMSats).toBe(250_000_000n);
    expect(fallbackAddress).toBe(undefined);
    expect(paymentHash).toBe(
      "0001020304050607080900010203040506070809000102030405060708090102",
    );
  });

  it("decodes bolt11 example-4", () => {
    const invoice =
      "lnbc20m1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp58yjmdan79s6qqdhdzgynm4zwqd5d7xmw5fk98klysy043l2ahrqs9qrsgq7ea976txfraylvgzuxs8kgcw23ezlrszfnh8r6qtfpr6cxga50aj6txm9rxrydzd06dfeawfk6swupvz4erwnyutnjq7x39ymw6j38gp7ynn44";
    const { amountMSats, fallbackAddress, paymentHash } =
      decodeInvoice(invoice);
    expect(amountMSats).toBe(2_000_000_000n);
    expect(fallbackAddress).toBe(undefined);
    expect(paymentHash).toBe(
      "0001020304050607080900010203040506070809000102030405060708090102",
    );
  });

  it("fails to decode bolt11 invoice with invalid checksum", () => {
    const invoice =
      "lnbc2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpquwpc4curk03c9wlrswe78q4eyqc7d8d0xqzpuyk0sg5g70me25alkluzd2x62aysf2pyy8edtjeevuv4p2d5p76r4zkmneet7uvyakky2zr4cusd45tftc9c5fh0nnqpnl2jfll544esqchsrnt";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("Invalid checksum");
  });

  it("fails to decode a bolt11 invoice that is too short", () => {
    const invoice =
      "lnbc1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6na6hlh";

    expect(() => {
      decodeInvoice(invoice);
    }).toThrow();
  });

  it("fails to decode malformed Bolt11 invoice - no prefix/data separator", () => {
    const invoice =
      "pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpquwpc4curk03c9wlrswe78q4eyqc7d8d0xqzpuyk0sg5g70me25alkluzd2x62aysf2pyy8edtjeevuv4p2d5p76r4zkmneet7uvyakky2zr4cusd45tftc9c5fh0nnqpnl2jfll544esqchsrny";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("Not a proper lightning payment request");
  });

  it("fails to decode malformed Bolt11 invoice - mixed case", () => {
    const invoice =
      "LNBC2500u1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpquwpc4curk03c9wlrswe78q4eyqc7d8d0xqzpuyk0sg5g70me25alkluzd2x62aysf2pyy8edtjeevuv4p2d5p76r4zkmneet7uvyakky2zr4cusd45tftc9c5fh0nnqpnl2jfll544esqchsrny";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("String must be lowercase or uppercase");
  });

  it("fails to decode bolt11 invoice with invalid multiplier", () => {
    const invoice =
      "lnbc2500x1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpusp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygs9qrsgqrrzc4cvfue4zp3hggxp47ag7xnrlr8vgcmkjxk3j5jqethnumgkpqp23z9jclu3v0a7e0aruz366e9wqdykw6dxhdzcjjhldxq0w6wgqcnu43j";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("Not a valid multiplier for the amount");
  });

  it("fails to decode bolt11 invoice with invalid submillisatoshi precision", () => {
    const invoice =
      "lnbc2500000001p1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpusp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygs9qrsgq0lzc236j96a95uv0m3umg28gclm5lqxtqqwk32uuk4k6673k6n5kfvx3d2h8s295fad45fdhmusm8sjudfhlf6dcsxmfvkeywmjdkxcp99202x";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("Amount is outside of valid range");
  });

  it("fails to decode bolt11 invoice with invalid payment secret", () => {
    const invoice =
      "lnbc20m1pvjluezpp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqhp58yjmdan79s6qqdhdzgynm4zwqd5d7xmw5fk98klysy043l2ahrqs9qrsgq7ea976txfraylvgzuxs8kgcw23ezlrszfnh8r6qtfpr6cxga50aj6txm9rxrydzd06dfeawfk6swupvz4erwnyutnjq7x39ymw6j38gp49qdkj";
    expect(() => {
      decodeInvoice(invoice);
    }).toThrow("Invalid payment secret");
  });

  it("decodes a bolt11 invoice with spark address embedded", () => {
    const invoice =
      "lnbc13u1p5xalmkpp5z79uwgne7znz76plf0q4zxmh8t3wke6gsnm5kn67h4satpgflkmssp5azht5ywc5s4m40jf9h0nwlr959a34n72pns50lfm93zz8lvs7nqsxq9z0rgqnp4q0p92sfan5vj2a4f8q3gsfsy8qp60maeuxz858c5x0hvt5u0p0h9jr9yqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqzfv7u6g85qqqqqqqqqqthqq9qpz9cat0ndmwmfx036y9fxfhdufta3mn95ta9xw34ynlwg7euxjck85ysq0gfqqqqq7u6egqrhxk2qqn3qqcqzpgdq2w3jhxap3xv9qyyssqfahd64hu0lffl7cw2e4evu400s09yeupypvnfjvjjyq8rh05y9gzd3dqnmkvuyd9jszyhmdey75dujz8xaufgahsxkqktf3wxny8ghsqpk4mg8";

    const { amountMSats, fallbackAddress, paymentHash, signedPayeePubkey } =
      decodeInvoice(invoice);

    expect(amountMSats).toBe(1300000n);
    expect(fallbackAddress).toBe(
      "0222e3ab7cdbb76d267c7442a4c9bb7895f63b9968be94ce8d493fb91ecf0d2c58",
    );
    expect(paymentHash).toBe(
      "178bc72279f0a62f683f4bc1511b773ae2eb674884f74b4f5ebd61d58509fdb7",
    );
    expect(signedPayeePubkey).toBe(
      "03c255413d9d192576a938228826043803a7efb9e1847a1f1433eec5d38f0bee59",
    );
  });

  // Word 265 is the spark receiver pubkey in the sentinel route hint, the field a
  // preferSpark payment is routed to.
  it("rejects an invoice whose spark fallback was rewritten in transit", () => {
    const invoice =
      "lnbc13u1p5xalmkpp5z79uwgne7znz76plf0q4zxmh8t3wke6gsnm5kn67h4satpgflkmssp5azht5ywc5s4m40jf9h0nwlr959a34n72pns50lfm93zz8lvs7nqsxq9z0rgqnp4q0p92sfan5vj2a4f8q3gsfsy8qp60maeuxz858c5x0hvt5u0p0h9jr9yqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqzfv7u6g85qqqqqqqqqqthqq9qpz9cat0ndmwmfx036y9fxfhdufta3mn95ta9xw34ynlwg7euxjck85ysq0gfqqqqq7u6egqrhxk2qqn3qqcqzpgdq2w3jhxap3xv9qyyssqfahd64hu0lffl7cw2e4evu400s09yeupypvnfjvjjyq8rh05y9gzd3dqnmkvuyd9jszyhmdey75dujz8xaufgahsxkqktf3wxny8ghsqpk4mg8";

    expect(() => {
      decodeInvoice(tamperWithInvoice(invoice, 265));
    }).toThrow("Invoice signature does not match invoice payee");
  });

  it("rejects an invoice with a malformed signature", () => {
    const invoice =
      "lnbc13u1p5xalmkpp5z79uwgne7znz76plf0q4zxmh8t3wke6gsnm5kn67h4satpgflkmssp5azht5ywc5s4m40jf9h0nwlr959a34n72pns50lfm93zz8lvs7nqsxq9z0rgqnp4q0p92sfan5vj2a4f8q3gsfsy8qp60maeuxz858c5x0hvt5u0p0h9jr9yqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqzfv7u6g85qqqqqqqqqqthqq9qpz9cat0ndmwmfx036y9fxfhdufta3mn95ta9xw34ynlwg7euxjck85ysq0gfqqqqq7u6egqrhxk2qqn3qqcqzpgdq2w3jhxap3xv9qyyssqfahd64hu0lffl7cw2e4evu400s09yeupypvnfjvjjyq8rh05y9gzd3dqnmkvuyd9jszyhmdey75dujz8xaufgahsxkqktf3wxny8ghsqpk4mg8";

    expect(() => {
      decodeInvoice(corruptInvoiceSignature(invoice));
    }).toThrow("Invalid signature found in invoice");
  });

  it("reports no signed payee for invoices without a payee tag", () => {
    const invoice =
      "lnbc2500u1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpu9qrsgquk0rl77nj30yxdy8j9vdx85fkpmdla2087ne0xh8nhedh8w27kyke0lp53ut353s06fv3qfegext0eh0ymjpf39tuven09sam30g4vgpfna3rh";

    expect(decodeInvoice(invoice).signedPayeePubkey).toBeUndefined();
  });
});
