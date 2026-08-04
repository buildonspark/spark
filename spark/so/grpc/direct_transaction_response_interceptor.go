package grpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/logging"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/grpcutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.uber.org/zap"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	sparkServicePrefix    = "/spark.SparkService/"
	sparkSspServicePrefix = "/spark_ssp_internal.SparkSspInternalService/"
)

var (
	directTransactionResponses = newDirectTransactionResponseRegistry(
		pbspark.File_spark_proto.Services().ByName("SparkService"),
	)
	directTransactionWitnessSanitizationFailures metric.Int64Counter
)

func init() {
	meter := otel.GetMeterProvider().Meter("spark.grpc")
	counter, err := meter.Int64Counter(
		"rpc.server.direct_transaction_witness_sanitization_failures",
		metric.WithDescription("Count of direct transaction fields cleared because their witnesses could not be sanitized"),
		metric.WithUnit("{count}"),
	)
	if err != nil {
		otel.Handle(err)
		if counter == nil {
			counter = noop.Int64Counter{}
		}
	}
	directTransactionWitnessSanitizationFailures = counter
}

func DirectTransactionResponseInterceptor() googlegrpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
		response, err := handler(ctx, req)
		if err != nil || !shouldStripDirectTransactionWitnesses(info.FullMethod) {
			return response, err
		}
		return stripDirectTransactionWitnesses(ctx, info.FullMethod, response), nil
	}
}

func DirectTransactionResponseStreamInterceptor() googlegrpc.StreamServerInterceptor {
	return func(srv any, stream googlegrpc.ServerStream, info *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
		if !shouldStripDirectTransactionWitnesses(info.FullMethod) {
			return handler(srv, stream)
		}
		return handler(srv, &directTransactionResponseServerStream{
			ServerStream: stream,
			fullMethod:   info.FullMethod,
		})
	}
}

type directTransactionResponseServerStream struct {
	googlegrpc.ServerStream
	fullMethod string
}

func (s *directTransactionResponseServerStream) SendMsg(response any) error {
	return s.ServerStream.SendMsg(stripDirectTransactionWitnesses(s.Context(), s.fullMethod, response))
}

func shouldStripDirectTransactionWitnesses(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, sparkServicePrefix) || strings.HasPrefix(fullMethod, sparkSspServicePrefix)
}

func stripDirectTransactionWitnesses(ctx context.Context, fullMethod string, response any) any {
	message, ok := response.(proto.Message)
	if !ok || message == nil || !directTransactionResponses.canContainDirectTransaction(message.ProtoReflect().Descriptor()) {
		return response
	}

	stripped := proto.Clone(message)
	stripDirectTransactionWitnessesFromMessage(ctx, fullMethod, stripped.ProtoReflect())
	return stripped
}

func stripDirectTransactionWitnessesFromMessage(ctx context.Context, fullMethod string, message protoreflect.Message) {
	messageName := message.Descriptor().FullName()
	for _, field := range directTransactionResponses.directFields[messageName] {
		stripTransactionWitness(ctx, fullMethod, message, field)
	}

	for _, field := range directTransactionResponses.nestedFieldsToStrip[messageName] {
		if !message.Has(field) {
			continue
		}
		value := message.Get(field)
		if field.IsMap() {
			value.Map().Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
				stripDirectTransactionWitnessesFromMessage(ctx, fullMethod, value.Message())
				return true
			})
			continue
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				stripDirectTransactionWitnessesFromMessage(ctx, fullMethod, list.Get(i).Message())
			}
			continue
		}
		stripDirectTransactionWitnessesFromMessage(ctx, fullMethod, value.Message())
	}
}

func stripTransactionWitness(
	ctx context.Context,
	fullMethod string,
	message protoreflect.Message,
	field protoreflect.FieldDescriptor,
) {
	rawTx := message.Get(field).Bytes()
	if len(rawTx) == 0 {
		return
	}
	tx, err := common.TxFromRawTxBytes(rawTx)
	if err == nil {
		rawTx, err = common.SerializeTxNoWitness(tx)
	}
	if err != nil {
		message.Clear(field)
		recordDirectTransactionWitnessSanitizationFailure(ctx, fullMethod, message.Descriptor(), field, err)
		return
	}
	message.Set(field, protoreflect.ValueOfBytes(rawTx))
}

func recordDirectTransactionWitnessSanitizationFailure(
	ctx context.Context,
	fullMethod string,
	message protoreflect.MessageDescriptor,
	field protoreflect.FieldDescriptor,
	err error,
) {
	attrs := grpcutil.ParseFullMethod(fullMethod)
	attrs = append(attrs,
		attribute.String("proto_message", string(message.FullName())),
		attribute.String("proto_field", string(field.Name())),
	)
	directTransactionWitnessSanitizationFailures.Add(ctx, 1, metric.WithAttributes(attrs...))
	logging.GetLoggerFromContext(ctx).With(zap.Error(err)).Sugar().Warnf(
		"failed to sanitize direct transaction witness for %s.%s in %s; clearing field",
		message.FullName(),
		field.Name(),
		fullMethod,
	)
}

type directTransactionResponseRegistry struct {
	directFields        map[protoreflect.FullName][]protoreflect.FieldDescriptor
	nestedFields        map[protoreflect.FullName][]protoreflect.FieldDescriptor
	nestedFieldsToStrip map[protoreflect.FullName][]protoreflect.FieldDescriptor
	messages            map[protoreflect.FullName]protoreflect.MessageDescriptor
	canContain          map[protoreflect.FullName]struct{}
}

func newDirectTransactionResponseRegistry(services ...protoreflect.ServiceDescriptor) *directTransactionResponseRegistry {
	registry := &directTransactionResponseRegistry{
		directFields:        make(map[protoreflect.FullName][]protoreflect.FieldDescriptor),
		nestedFields:        make(map[protoreflect.FullName][]protoreflect.FieldDescriptor),
		nestedFieldsToStrip: make(map[protoreflect.FullName][]protoreflect.FieldDescriptor),
		messages:            make(map[protoreflect.FullName]protoreflect.MessageDescriptor),
		canContain:          make(map[protoreflect.FullName]struct{}),
	}
	for _, service := range services {
		registry.addService(service)
	}
	return registry
}

func (r *directTransactionResponseRegistry) addService(service protoreflect.ServiceDescriptor) {
	if service == nil {
		panic("direct transaction response service descriptor is nil")
	}
	methods := service.Methods()
	for i := 0; i < methods.Len(); i++ {
		r.collectMessage(methods.Get(i).Output())
	}
	r.rebuildReachability()
}

func (r *directTransactionResponseRegistry) collectMessage(message protoreflect.MessageDescriptor) {
	messageName := message.FullName()
	if _, ok := r.messages[messageName]; ok {
		return
	}
	r.messages[messageName] = message

	fields := message.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if isDirectTransactionFieldName(field.Name()) {
			if field.Kind() != protoreflect.BytesKind || field.IsList() || field.IsMap() {
				panic(fmt.Sprintf("direct transaction field %s.%s must be singular bytes", messageName, field.Name()))
			}
			r.directFields[messageName] = append(r.directFields[messageName], field)
		}

		nestedMessage := nestedMessageDescriptor(field)
		if nestedMessage == nil {
			continue
		}
		r.nestedFields[messageName] = append(r.nestedFields[messageName], field)
		r.collectMessage(nestedMessage)
	}
}

func (r *directTransactionResponseRegistry) rebuildReachability() {
	r.canContain = make(map[protoreflect.FullName]struct{}, len(r.messages))
	for messageName, fields := range r.directFields {
		if len(fields) > 0 {
			r.canContain[messageName] = struct{}{}
		}
	}

	for changed := true; changed; {
		changed = false
		for messageName, fields := range r.nestedFields {
			if _, ok := r.canContain[messageName]; ok {
				continue
			}
			for _, field := range fields {
				if _, ok := r.canContain[nestedMessageDescriptor(field).FullName()]; ok {
					r.canContain[messageName] = struct{}{}
					changed = true
					break
				}
			}
		}
	}

	r.nestedFieldsToStrip = make(map[protoreflect.FullName][]protoreflect.FieldDescriptor, len(r.canContain))
	for messageName, fields := range r.nestedFields {
		for _, field := range fields {
			if _, ok := r.canContain[nestedMessageDescriptor(field).FullName()]; ok {
				r.nestedFieldsToStrip[messageName] = append(r.nestedFieldsToStrip[messageName], field)
			}
		}
	}
}

func (r *directTransactionResponseRegistry) canContainDirectTransaction(message protoreflect.MessageDescriptor) bool {
	_, ok := r.canContain[message.FullName()]
	return ok
}

func isDirectTransactionFieldName(name protoreflect.Name) bool {
	fieldName := string(name)
	return strings.HasSuffix(fieldName, "_tx") &&
		(strings.HasPrefix(fieldName, "direct_") || strings.HasPrefix(fieldName, "intermediate_direct_"))
}

func nestedMessageDescriptor(field protoreflect.FieldDescriptor) protoreflect.MessageDescriptor {
	if field.IsMap() {
		value := field.MapValue()
		if value.Kind() == protoreflect.MessageKind || value.Kind() == protoreflect.GroupKind {
			return value.Message()
		}
		return nil
	}
	if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
		return field.Message()
	}
	return nil
}
