export function findNotFirstUsingFind<T>(arr: Buffer[]): Buffer | undefined {
  return arr.find((item, index) => index !== 0 && Buffer.compare(item, arr[0]));
}
