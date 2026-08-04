//go:build lightspark

package grpc

import pbssp "github.com/lightsparkdev/spark/proto/spark_ssp_internal"

func init() {
	directTransactionResponses.addService(
		pbssp.File_spark_ssp_internal_proto.Services().ByName("SparkSspInternalService"),
	)
}
