package main

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/lightsparkdev/spark/so"
	"github.com/lightsparkdev/spark/so/rpcpolicy"
)

// TestInternalListenerServicesAreOperatorBrontide is the authoritative drift check between the services actually
// registered on the brontide-authenticated internal listener and the AuthOperatorBrontide policies in rpcpolicy. It
// registers into a real grpc.Server and reads back the registration, so moving a service into or out of
// RegisterInternalGrpcServers fails here until the policy table follows.
func TestInternalListenerServicesAreOperatorBrontide(t *testing.T) {
	server := grpc.NewServer()
	require.NoError(t, RegisterInternalGrpcServers(server, &args{}, &so.Config{}, nil, nil))

	internal := map[string]struct{}{}
	for serviceName, info := range server.GetServiceInfo() {
		for _, m := range info.Methods {
			internal["/"+serviceName+"/"+m.Name] = struct{}{}
		}
	}
	require.NotEmpty(t, internal, "no methods registered by RegisterInternalGrpcServers")

	var missingLabel, unexpectedLabel []string
	for _, method := range rpcpolicy.RegisteredMethods() {
		p, ok := rpcpolicy.LookUp(method)
		require.True(t, ok)
		_, isInternal := internal[method]
		switch {
		case isInternal && p.AuthMode != rpcpolicy.AuthOperatorBrontide:
			missingLabel = append(missingLabel, method)
		case !isInternal && p.AuthMode == rpcpolicy.AuthOperatorBrontide:
			unexpectedLabel = append(unexpectedLabel, method)
		}
	}
	slices.Sort(missingLabel)
	slices.Sort(unexpectedLabel)

	require.Empty(t, missingLabel,
		"methods registered on the internal brontide listener must be AuthOperatorBrontide: %v", missingLabel)
	require.Empty(t, unexpectedLabel,
		"AuthOperatorBrontide is only valid for services registered by RegisterInternalGrpcServers; these aren't: %v", unexpectedLabel)
}
