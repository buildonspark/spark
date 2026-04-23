package handler

import (
	"context"
	"fmt"
	"time"

	pb "github.com/lightsparkdev/spark/proto/spark"
	sparkerrors "github.com/lightsparkdev/spark/so/errors"
	"github.com/lightsparkdev/spark/so/partner"
)

// PartnerQueryHandler handles partner transaction volume queries against RisingWave.
type PartnerQueryHandler struct {
	rwClient *partner.RisingWaveClient
}

// NewPartnerQueryHandler creates a new PartnerQueryHandler.
// rwClient may be nil if RisingWave is not configured.
func NewPartnerQueryHandler(rwClient *partner.RisingWaveClient) *PartnerQueryHandler {
	return &PartnerQueryHandler{rwClient: rwClient}
}

// QuerySparkTransactionVolumes returns aggregated transaction volumes for the
// authenticated partner. Requires a valid partner JWT in the request context.
func (h *PartnerQueryHandler) QuerySparkTransactionVolumes(
	ctx context.Context,
	req *pb.QuerySparkTransactionVolumesRequest,
) (*pb.QuerySparkTransactionVolumesResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(err)
	}

	pInfo, ok := partner.GetPartnerInfoFromContext(ctx)
	if !ok {
		return nil, sparkerrors.PermissionDeniedNoReadAccess(
			fmt.Errorf("partner JWT required for transaction volume queries"),
		)
	}

	start, err := time.Parse(time.DateOnly, req.StartDate)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("start_date must be YYYY-MM-DD: %w", err),
		)
	}
	end, err := time.Parse(time.DateOnly, req.EndDate)
	if err != nil {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("end_date must be YYYY-MM-DD: %w", err),
		)
	}
	if start.After(end) {
		return nil, sparkerrors.InvalidArgumentMalformedField(
			fmt.Errorf("start_date must not be after end_date"),
		)
	}

	if h.rwClient == nil {
		return nil, sparkerrors.UnavailableDataStore(
			fmt.Errorf("transaction volume query is not configured"),
		)
	}

	var txTypeFilter []string
	for _, t := range req.TransactionTypes {
		mapped := mapTransactionType(t)
		if mapped == "" {
			return nil, sparkerrors.InvalidArgumentMalformedField(
				fmt.Errorf("invalid transaction_type: %s", t.String()),
			)
		}
		txTypeFilter = append(txTypeFilter, mapped)
	}

	var networkFilter *string
	if req.Network != nil {
		mapped := mapNetwork(*req.Network)
		if mapped == "" {
			return nil, sparkerrors.InvalidArgumentMalformedField(
				fmt.Errorf("invalid network: %s", req.Network.String()),
			)
		}
		networkFilter = &mapped
	}

	rows, err := h.rwClient.QueryTransactionVolumes(
		ctx, pInfo.PartnerID, pInfo.Label, start, end, txTypeFilter, networkFilter,
	)
	if err != nil {
		return nil, sparkerrors.InternalDatabaseReadError(
			fmt.Errorf("failed to query transaction volumes: %w", err),
		)
	}

	var totalVolume int64
	var totalCount int64
	var txTypes []*pb.SparkTransactionVolume
	for _, row := range rows {
		protoType := mapTransactionTypeToProto(row.TransactionType)
		txTypes = append(txTypes, &pb.SparkTransactionVolume{
			TransactionType:  protoType,
			VolumeSats:       row.VolumeSats,
			TransactionCount: row.TransactionCount,
		})
		totalVolume += row.VolumeSats
		totalCount += row.TransactionCount
	}

	return &pb.QuerySparkTransactionVolumesResponse{
		PartnerId:             pInfo.PartnerID,
		Label:                 pInfo.Label,
		StartDate:             req.StartDate,
		EndDate:               req.EndDate,
		TransactionTypes:      txTypes,
		TotalVolumeSats:       totalVolume,
		TotalTransactionCount: totalCount,
	}, nil
}

func mapTransactionType(t pb.SparkTransactionType) string {
	switch t {
	case pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_TRANSFER:
		return "TRANSFER"
	case pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_LIGHTNING_SEND:
		return "LIGHTNING_SEND"
	case pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_LIGHTNING_RECEIVE:
		return "LIGHTNING_RECEIVE"
	case pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_COOPERATIVE_EXIT:
		return "COOPERATIVE_EXIT"
	case pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_DEPOSIT:
		return "DEPOSIT"
	default:
		return ""
	}
}

// mapNetwork maps a proto Network enum to its RisingWave string value.
// UNSPECIFIED returns "" so the handler rejects it (defense in depth beyond
// proto validation).
func mapNetwork(n pb.Network) string {
	switch n {
	case pb.Network_MAINNET:
		return "MAINNET"
	case pb.Network_REGTEST:
		return "REGTEST"
	case pb.Network_TESTNET:
		return "TESTNET"
	case pb.Network_SIGNET:
		return "SIGNET"
	default:
		return ""
	}
}

func mapTransactionTypeToProto(s string) pb.SparkTransactionType {
	switch s {
	case "TRANSFER":
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_TRANSFER
	case "LIGHTNING_SEND":
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_LIGHTNING_SEND
	case "LIGHTNING_RECEIVE":
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_LIGHTNING_RECEIVE
	case "COOPERATIVE_EXIT":
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_COOPERATIVE_EXIT
	case "DEPOSIT":
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_DEPOSIT
	default:
		return pb.SparkTransactionType_SPARK_TRANSACTION_TYPE_UNSPECIFIED
	}
}
