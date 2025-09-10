import { getSparkAddressFromTaproot } from "../utils/xchain-address.js";

describe("xchain-address", () => {
  it.each([
    [
      "mainnet",
      "bc1pvluhspufxmuus9wh3dshxhxfg3656c9mwfw85scaydyp7sk9800sl4h5ae",
      "sp1pgssyele0qrcjdheeq2a0zmpwdwvj3r4f4stkuju0fp36g6grapv2w7ltce4pm",
    ],
    [
      "testnet",
      "tb1psw24aa3jkdkndr68gy2gk3ws4hm7dnsccqqtd92fxxqv2jhv5fcqkfyu7v",
      "spt1pgss9qu4tmmr9vmdx685wsg53dzapt0hum8p3sqqk625jvvqc49wegns9pkyp6",
    ],
    [
      "regtest",
      "bcrt1p47kh0ff29d3rjw2n43vxqgmrgv9az562x37x6dp4ehyqq7ezyhcqlz5y42",
      "sprt1pgss9tadw7jj52mz8yu48tzcvq3kxsct69f55drud56rtnwgqpajyf0sp8jkjq",
    ],
  ])(
    "getSparkAddressFromTaproot success (%s)",
    (_network, taprootAddress, expectedSparkAddress) => {
      expect(getSparkAddressFromTaproot(taprootAddress)).toBe(
        expectedSparkAddress,
      );
    },
  );
});
