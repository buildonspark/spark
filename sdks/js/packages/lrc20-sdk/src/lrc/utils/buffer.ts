import { PARITY } from "./constants.ts";

export function reverseBuffer(buffer: Buffer): Buffer {
  if (buffer.length < 1) return buffer;
  let j = buffer.length - 1;
  let tmp = 0;
  let reversed = Buffer.from(buffer);
  for (let i = 0; i < reversed.length / 2; i++) {
    tmp = reversed[i];
    reversed[i] = reversed[j];
    reversed[j] = tmp;
    j--;
  }
  return reversed;
}

export const toXOnly = (pubKey: Buffer) => (pubKey.length === 32 ? pubKey : pubKey.slice(1, 33));

export const toEvenParity = (pubKey: Buffer) => {
  return Buffer.concat([PARITY, toXOnly(pubKey)]);
};
