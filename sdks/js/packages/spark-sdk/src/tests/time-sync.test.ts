import { describe, expect, it, jest } from "@jest/globals";
import { getMonotonicTime, ServerTimeSync } from "../services/time-sync.js";

describe("ServerTimeSync", () => {
  it("should return null when not yet synced", () => {
    const timeSync = new ServerTimeSync();
    expect(timeSync.getCurrentServerTime()).toBeNull();
    expect(timeSync.getOffset()).toBeNull();
    expect(timeSync.isSynced()).toBe(false);
  });

  it("should record and calculate server time correctly", () => {
    const timeSync = new ServerTimeSync();

    const serverTime = new Date("2024-01-01T12:00:00Z");
    const now = getMonotonicTime();
    const sendTime = now - 50;
    const receiveTime = now;
    const serverProcessingTimeMs = 4;

    timeSync.recordSync(
      serverTime.toUTCString(),
      serverProcessingTimeMs,
      sendTime,
      receiveTime,
    );

    expect(timeSync.isSynced()).toBe(true);

    const currentServerTime = timeSync.getCurrentServerTime();
    expect(currentServerTime).not.toBeNull();

    const roundTripTime = receiveTime - sendTime - serverProcessingTimeMs;
    const expectedServerTime = Math.floor(
      serverTime.getTime() + roundTripTime / 2,
    );
    expect(currentServerTime!.getTime()).toBeGreaterThanOrEqual(
      expectedServerTime,
    );
    expect(currentServerTime!.getTime()).toBeLessThan(expectedServerTime + 100);

    const offset = timeSync.getOffset();
    expect(offset).not.toBeNull();
    const expectedOffset = expectedServerTime - receiveTime;
    expect(offset).toBeCloseTo(expectedOffset, -1);
  });

  it("should calculate current time based on monotonic clock progression", () => {
    const timeSync = new ServerTimeSync();

    const serverTime = new Date("2024-01-01T12:00:00.000Z");
    const now = getMonotonicTime();
    const sendTime = now - 100;
    const receiveTime = now;
    const serverProcessingTimeMs = 13;

    timeSync.recordSync(
      serverTime.toUTCString(),
      serverProcessingTimeMs,
      sendTime,
      receiveTime,
    );

    const currentServerTime1 = timeSync.getCurrentServerTime();
    expect(currentServerTime1).not.toBeNull();

    const roundTripTime = receiveTime - sendTime - serverProcessingTimeMs;
    const expectedServerTime = Math.floor(
      serverTime.getTime() + roundTripTime / 2,
    );
    expect(currentServerTime1!.getTime()).toBeGreaterThanOrEqual(
      expectedServerTime,
    );
    expect(currentServerTime1!.getTime()).toBeLessThan(
      expectedServerTime + 100,
    );
  });

  it("should handle invalid date headers gracefully", () => {
    const timeSync = new ServerTimeSync();
    const consoleWarnSpy = jest
      .spyOn(console, "warn")
      .mockImplementation(() => {});

    const now = getMonotonicTime();
    timeSync.recordSync("invalid-date", 13, now - 100, now);

    expect(consoleWarnSpy).toHaveBeenCalledWith(
      "Invalid server date header:",
      "invalid-date",
    );
    expect(timeSync.isSynced()).toBe(false);

    consoleWarnSpy.mockRestore();
  });

  it("should reset all sync data", () => {
    const timeSync = new ServerTimeSync();

    const serverTime = new Date("2024-01-01T12:00:00Z");
    const now = getMonotonicTime();
    timeSync.recordSync(serverTime.toUTCString(), 12, now - 100, now);

    expect(timeSync.isSynced()).toBe(true);

    timeSync.reset();

    expect(timeSync.isSynced()).toBe(false);
    expect(timeSync.getCurrentServerTime()).toBeNull();
    expect(timeSync.getOffset()).toBeNull();
  });

  it("should be resistant to local time manipulation", () => {
    const timeSync = new ServerTimeSync();

    const serverTime = new Date("2024-01-01T12:00:00.000Z");
    const now = getMonotonicTime();
    const sendTime = now - 50;
    const receiveTime = now;
    const serverProcessingTimeMs = 4;

    timeSync.recordSync(
      serverTime.toUTCString(),
      serverProcessingTimeMs,
      sendTime,
      receiveTime,
    );

    const time1 = timeSync.getCurrentServerTime();
    expect(time1).not.toBeNull();

    const roundTripTime = receiveTime - sendTime - serverProcessingTimeMs;
    const expectedServerTime = Math.floor(
      serverTime.getTime() + roundTripTime / 2,
    );
    expect(time1!.getTime()).toBeGreaterThanOrEqual(expectedServerTime);
    expect(time1!.getTime()).toBeLessThan(expectedServerTime + 100);

    const realDateNow = Date.now;
    (Date.now as any) = () => 0;

    const time2 = timeSync.getCurrentServerTime();
    expect(time2).not.toBeNull();

    expect(time2!.getTime()).toBeGreaterThanOrEqual(time1!.getTime());
    const timeDiff = time2!.getTime() - time1!.getTime();
    expect(timeDiff).toBeLessThan(100);

    Date.now = realDateNow;
  });
});
