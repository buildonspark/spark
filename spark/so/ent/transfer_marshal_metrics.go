package ent

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// transferMarshalMissingEdgeCounter counts MarshalProto/MarshalProtoForReceiver
// invocations that found a participant edge (TransferSenders/TransferReceivers)
// nil while the multi-participant format knob was on. The output proto silently
// emits an empty Senders[]/Receivers[] in that case — drive this to zero, then
// promote to a hard error.
//
// Labels: caller={MarshalProto|MarshalProtoForReceiver}, edge={TransferSenders|TransferReceivers}.
var transferMarshalMissingEdgeCounter metric.Int64Counter

func init() {
	meter := otel.Meter("spark.transfer.marshal")
	var err error
	transferMarshalMissingEdgeCounter, err = meter.Int64Counter(
		"spark_transfer_marshal_missing_edge_total",
		metric.WithDescription("Times Transfer.MarshalProto-family found a participant edge nil with KnobReadMIMOMultiParticipantFormat on; goal: drive to zero then hard-error"),
		metric.WithUnit("{occurrences}"),
	)
	if err != nil {
		otel.Handle(err)
		if transferMarshalMissingEdgeCounter == nil {
			transferMarshalMissingEdgeCounter = noop.Int64Counter{}
		}
	}
}
