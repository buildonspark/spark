import { Lrc20Protos } from "@buildonspark/lrc20-sdk";
import {
  OperationType,
  OnChainTransactionStatus,
  SparkTransactionStatus,
  LayerType,
} from "../types.js";

// Helper functions to map from numeric enums to string enums
export function mapOperationType(
  type: Lrc20Protos.OperationType,
): OperationType {
  switch (type) {
    case Lrc20Protos.OperationType.USER_TRANSFER:
      return OperationType.USER_TRANSFER;
    case Lrc20Protos.OperationType.USER_BURN:
      return OperationType.USER_BURN;
    case Lrc20Protos.OperationType.ISSUER_ANNOUNCE:
      return OperationType.ISSUER_ANNOUNCE;
    case Lrc20Protos.OperationType.ISSUER_MINT:
      return OperationType.ISSUER_MINT;
    case Lrc20Protos.OperationType.ISSUER_TRANSFER:
      return OperationType.ISSUER_TRANSFER;
    case Lrc20Protos.OperationType.ISSUER_FREEZE:
      return OperationType.ISSUER_FREEZE;
    case Lrc20Protos.OperationType.ISSUER_UNFREEZE:
      return OperationType.ISSUER_UNFREEZE;
    case Lrc20Protos.OperationType.ISSUER_BURN:
      return OperationType.ISSUER_BURN;
    default:
      return OperationType.USER_TRANSFER; // Default case
  }
}

export function mapOnChainTransactionStatus(
  status: Lrc20Protos.OnChainTransactionStatus,
): OnChainTransactionStatus {
  switch (status) {
    case Lrc20Protos.OnChainTransactionStatus.PENDING:
      return OnChainTransactionStatus.PENDING;
    case Lrc20Protos.OnChainTransactionStatus.CONFIRMED:
      return OnChainTransactionStatus.CONFIRMED;
    case Lrc20Protos.OnChainTransactionStatus.WAITING_MINED:
      return OnChainTransactionStatus.WAITING_MINED;
    case Lrc20Protos.OnChainTransactionStatus.MINED:
      return OnChainTransactionStatus.MINED;
    case Lrc20Protos.OnChainTransactionStatus.ATTACHING:
      return OnChainTransactionStatus.ATTACHING;
    case Lrc20Protos.OnChainTransactionStatus.ATTACHED:
      return OnChainTransactionStatus.ATTACHED;
    default:
      return OnChainTransactionStatus.PENDING; // Default case
  }
}

export function mapSparkTransactionStatus(
  status: Lrc20Protos.SparkTransactionStatus,
): SparkTransactionStatus {
  switch (status) {
    case Lrc20Protos.SparkTransactionStatus.STARTED:
      return SparkTransactionStatus.STARTED;
    case Lrc20Protos.SparkTransactionStatus.SIGNED:
      return SparkTransactionStatus.SIGNED;
    case Lrc20Protos.SparkTransactionStatus.FINALIZED:
      return SparkTransactionStatus.FINALIZED;
    default:
      return SparkTransactionStatus.STARTED; // Default case
  }
}

export function mapLayer(layer: number): LayerType {
  switch (layer) {
    case 0: // L1
      return LayerType.L1;
    case 1: // SPARK
      return LayerType.SPARK;
    default:
      return LayerType.L1; // Default case
  }
}
