package dkg

import (
	"context"
	"sync"

	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbdkg "github.com/lightsparkdev/spark-go/proto/dkg"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	"github.com/lightsparkdev/spark-go/so"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server is the grpc server for the DKG protocol.
// It is only used by the signing operators.
type Server struct {
	pbdkg.UnimplementedDKGServiceServer
	frostConnection *grpc.ClientConn
	state           *States
	config          *so.Config
}

// NewServer creates a new DKG server based on the given config.
func NewServer(frostConnection *grpc.ClientConn, config *so.Config) *Server {
	return &Server{
		state:           &States{},
		frostConnection: frostConnection,
		config:          config,
	}
}

func (s *Server) StartDkg(ctx context.Context, req *pbdkg.StartDkgRequest) (*emptypb.Empty, error) {
	if err := GenerateKeys(ctx, s.config, uint64(req.Count)); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// InitiateDkg initiates the DKG protocol.
// It will be called by the coordinator. It will start the DKG round 1 and deliver the round 1 package to the coordinator.
func (s *Server) InitiateDkg(ctx context.Context, req *pbdkg.InitiateDkgRequest) (*pbdkg.InitiateDkgResponse, error) {
	if err := s.state.InitiateDkg(req.RequestId, req.MaxSigners, req.MinSigners, req.CoordinatorIndex); err != nil {
		return nil, err
	}

	frostClient := pbfrost.NewFrostServiceClient(s.frostConnection)
	round1Response, err := frostClient.DkgRound1(ctx, &pbfrost.DkgRound1Request{
		RequestId:  req.RequestId,
		Identifier: s.config.Identifier,
		MaxSigners: req.MaxSigners,
		MinSigners: req.MinSigners,
		KeyCount:   req.KeyCount,
	})
	if err != nil {
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	if err := s.state.ProvideRound1Package(req.RequestId, round1Response.Round1Packages); err != nil {
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	return &pbdkg.InitiateDkgResponse{
		Identifier:    s.config.Identifier,
		Round1Package: round1Response.Round1Packages,
	}, nil
}

// Round1Packages receives the round 1 packages from the coordinator.
// It will be called by the coordinator. This function will deliver the round 1 packages from the other operators.
// The packages will be signed with this operator's identity key and sent the signature back to the coordinator.
// It is used as a confirmation that the operator has received the round 1 packages.
func (s *Server) Round1Packages(_ context.Context, req *pbdkg.Round1PackagesRequest) (*pbdkg.Round1PackagesResponse, error) {
	round1Packages := make([]map[string][]byte, len(req.Round1Packages))
	for i, p := range req.Round1Packages {
		round1Packages[i] = p.Packages
	}

	if err := s.state.ReceivedRound1Packages(req.RequestId, s.config.Identifier, round1Packages); err != nil {
		return nil, err
	}

	signature, err := signRound1Packages(s.config.IdentityPrivateKey, round1Packages)
	if err != nil {
		return nil, err
	}

	return &pbdkg.Round1PackagesResponse{
		Identifier:      s.config.Identifier,
		Round1Signature: signature,
	}, nil
}

// Round1Signature receives the round 1 signatures from the coordinator.
// It will be called by the coordinator. This function will validate the round 1 signatures of all other operators to make sure everyone receives the same round 1 packages.
// Then it will start the DKG round 2, and distribute the round 2 package to the corresponding operators.
func (s *Server) Round1Signature(ctx context.Context, req *pbdkg.Round1SignatureRequest) (*pbdkg.Round1SignatureResponse, error) {
	validationFailures, err := s.state.ReceivedRound1Signature(req.RequestId, s.config.Identifier, req.Round1Signatures, s.config.SigningOperatorMap)
	if err != nil {
		return nil, err
	}

	if len(validationFailures) > 0 {
		return &pbdkg.Round1SignatureResponse{
			Identifier:         s.config.Identifier,
			ValidationFailures: validationFailures,
		}, nil
	}

	state, err := s.state.GetState(req.RequestId)
	if err != nil {
		return nil, err
	}

	round1PackagesMaps := make([]*pbcommon.PackageMap, len(state.ReceivedRound1Packages))
	for i, p := range state.ReceivedRound1Packages {
		delete(p, s.config.Identifier)
		round1PackagesMaps[i] = &pbcommon.PackageMap{Packages: p}
	}

	frostClient := pbfrost.NewFrostServiceClient(s.frostConnection)
	round2Response, err := frostClient.DkgRound2(ctx, &pbfrost.DkgRound2Request{
		RequestId:          req.RequestId,
		Round1PackagesMaps: round1PackagesMaps,
	})
	if err != nil {
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	var wg sync.WaitGroup
	// Distribute the round 2 package to all participants
	for identifier := range round2Response.Round2Packages[0].Packages {
		operator := s.config.SigningOperatorMap[identifier]
		wg.Add(1)
		go func(identifier string, addr string) {
			defer wg.Done()
			connection, err := common.NewGRPCConnection(addr, operator.CertPath)
			if err != nil {
				return
			}
			defer connection.Close()

			client := pbdkg.NewDKGServiceClient(connection)

			round2Packages := make([][]byte, len(round2Response.Round2Packages))
			for i, p := range round2Response.Round2Packages {
				round2Packages[i] = p.Packages[identifier]
			}

			round2Signature, err := signRound2Packages(s.config.IdentityPrivateKey, round2Packages)
			if err != nil {
				return
			}

			_, err = client.Round2Packages(ctx, &pbdkg.Round2PackagesRequest{
				RequestId:       req.RequestId,
				Identifier:      s.config.Identifier,
				Round2Packages:  round2Packages,
				Round2Signature: round2Signature,
			})
			if err != nil {
				return
			}
		}(identifier, operator.Address)
	}

	wg.Wait()

	if err := s.state.ProceedToRound3(ctx, req.RequestId, s.frostConnection, s.config); err != nil {
		return nil, err
	}

	return &pbdkg.Round1SignatureResponse{
		Identifier: s.config.Identifier,
	}, nil
}

// Round2Packages receives the round 2 packages from other operators.
// Once all operators have sent their round 2 packages, it will start the DKG round 3 and store the key shares in the database.
func (s *Server) Round2Packages(ctx context.Context, req *pbdkg.Round2PackagesRequest) (*pbdkg.Round2PackagesResponse, error) {
	if req.Identifier == s.config.Identifier {
		return &pbdkg.Round2PackagesResponse{}, nil
	}

	if err := s.state.ReceivedRound2Packages(req.RequestId, req.Identifier, req.Round2Packages, req.Round2Signature, s.frostConnection, s.config); err != nil {
		return nil, err
	}

	if err := s.state.ProceedToRound3(ctx, req.RequestId, s.frostConnection, s.config); err != nil {
		return nil, err
	}

	return &pbdkg.Round2PackagesResponse{}, nil
}
