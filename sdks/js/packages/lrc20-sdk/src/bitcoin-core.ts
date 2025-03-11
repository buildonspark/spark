import "./buffer";
// import ecc from "@bitcoinerlab/secp256k1";
import * as bitcoin from "bitcoinjs-lib";

import * as ecc from "@bitcoinerlab/secp256k1";
import { ECPairFactory } from "ecpair";

bitcoin.initEccLib(ecc);
const ECPair = ECPairFactory(ecc);
export { bitcoin, ecc, ECPair };
