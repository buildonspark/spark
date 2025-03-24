import { initEccLib } from "bitcoinjs-lib";

import * as ecc from "@bitcoinerlab/secp256k1";
import { ECPairFactory } from "ecpair";

initEccLib(ecc);
const ECPair = ECPairFactory(ecc);
export { ecc, ECPair };
