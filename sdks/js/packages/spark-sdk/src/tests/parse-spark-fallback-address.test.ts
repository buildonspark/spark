import { describe, expect, it } from "@jest/globals";
import { parseSparkFallbackAddress } from "../services/bolt11-spark.js";

describe("parseSparkFallbackAddress", () => {
  it("should parse spark fallback address with tagCode and words", () => {
    const value = {
      tagCode: 9,
      words:
        "unknown1lwdcxzuntdschqemnwdu8w6npvcmrv7rwvesnsarewf4rj7fedehxzdt5d3khzvnjdeu8gcf4w3e8z6mxddaxwa3kwen8w6r4vak8v7n8deehxut8dfa8zut9vduk2em2xaers7rgw448v6rwvs6rsetywvcxzm34wfckw6rgv9kxg6nr0pa85ut8xpm8jem30yehz7r60pehqaty09ck27rrd468samgvs68qmtywenhga3jdd6xz6ecwqm8qcmrvdekkunxdfax2af5vahxverhw3urvwrxvvuxv7nkd5m8junyv5682vm6ddmhzatkxfjxwertx5ukvmtw09shwatkv4sngvn8v9khxurh0pckcergwpm827n3dde8vvm5wgspxv07",
    };

    const result = parseSparkFallbackAddress(value);
    expect(result).toBeDefined();
    expect(result).toMatch(/^sparkl1/);
  });

  it("should return undefined for null value", () => {
    expect(parseSparkFallbackAddress(null)).toBeUndefined();
  });

  it("should return undefined for undefined value", () => {
    expect(parseSparkFallbackAddress(undefined)).toBeUndefined();
  });

  it("should return string value as-is", () => {
    const value = "test-string";
    expect(parseSparkFallbackAddress(value)).toBe(value);
  });

  it("should return undefined for object without words", () => {
    const value = { tagCode: 9 };
    expect(parseSparkFallbackAddress(value)).toBeUndefined();
  });

  it("should return undefined for object with non-string words", () => {
    const value = { tagCode: 9, words: 123 };
    expect(parseSparkFallbackAddress(value)).toBeUndefined();
  });
});
