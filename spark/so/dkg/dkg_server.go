package dkg

import (
	"context"

	frost "github.com/lightsparkdev/spark-go/frost"
	pb "github.com/lightsparkdev/spark-go/proto"
	"github.com/lightsparkdev/spark-go/so"
)

type DkgServer struct {
	pb.UnimplementedDKGServiceServer
	frostClient frost.FrostClient
	state       *DkgStates
	config      *so.Config
}

func NewDkgServer(frostClient frost.FrostClient, config *so.Config) *DkgServer {
	return &DkgServer{
		state:       &DkgStates{},
		frostClient: frostClient,
		config:      config,
	}
}

func (s *DkgServer) InitiateDkg(ctx context.Context, req *pb.InitiateDkgRequest) (*pb.InitiateDkgResponse, error) {
	if err := s.state.InitiateDkg(req.RequestId, req.MaxSigners); err != nil {
		return nil, err
	}

	round1Response, err := s.frostClient.Client.DkgRound1(ctx, &pb.DkgRound1Request{
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
		return nil, err
	}

	return &pb.InitiateDkgResponse{
		Identifier:    s.config.Identifier,
		Round1Package: round1Response.Round1Packages,
	}, nil
}

func (s *DkgServer) ReceivedRound1Packages(ctx context.Context, req *pb.Round1PackagesRequest) (*pb.Round1PackagesResponse, error) {
	round1Packages := make([]map[string][]byte, len(req.Round1Packages))
	for i, p := range req.Round1Packages {
		round1Packages[i] = p.Packages
	}

	if err := s.state.ReceivedRound1Packages(req.RequestId, s.config.Identifier, round1Packages); err != nil {
		return nil, err
	}

	signature, err := SignRound1Packages(s.config.PrivateKey, round1Packages)
	if err != nil {
		return nil, err
	}

	return &pb.Round1PackagesResponse{
		Identifier:      s.config.Identifier,
		Round1Signature: signature,
	}, nil
}

func (s *DkgServer) ReceivedRound1Signatures(ctx context.Context, req *pb.Round1SignatureRequest) (*pb.Round1SignatureResponse, error) {
	validationFailures, err := s.state.ReceivedRound1Signature(req.RequestId, s.config.Identifier, req.Round1Signatures, s.config.PublicKeyMap)
	if err != nil {
		return nil, err
	}

	if validationFailures != nil && len(validationFailures) > 0 {
		return &pb.Round1SignatureResponse{
			Identifier:         s.config.Identifier,
			ValidationFailures: validationFailures,
		}, nil
	}

	state, err := s.state.GetState(req.RequestId)
	if err != nil {
		return nil, err
	}

	round1PackagesMaps := make([]*pb.PackageMap, len(state.ReceivedRound1Packages))
	for i, p := range state.ReceivedRound1Packages {
		round1PackagesMaps[i] = &pb.PackageMap{Packages: p}
	}

	round2Response, err := s.frostClient.Client.DkgRound2(ctx, &pb.DkgRound2Request{
		RequestId:          req.RequestId,
		Round1PackagesMaps: round1PackagesMaps,
	})
	if err != nil {
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	// Distribute the round 2 package to all participants
	for addr := range s.config.SigningOperatorAddressMap {
		client, err := NewDKGServiceClient(addr)
		if err != nil {
			return nil, err
		}

		round2Packages := make([][]byte, len(round2Response.Round2Packages))
		for i, p := range round2Response.Round2Packages {
			round2Packages[i] = p.Packages[s.config.Identifier]
		}

		round2Signature, err := SignRound2Packages(s.config.PrivateKey, round2Packages)
		if err != nil {
			return nil, err
		}

		client.Client.Round2Packages(ctx, &pb.Round2PackagesRequest{
			RequestId:       req.RequestId,
			Identifier:      s.config.Identifier,
			Round2Packages:  round2Packages,
			Round2Signature: round2Signature,
		})
	}

	return &pb.Round1SignatureResponse{
		Identifier: s.config.Identifier,
	}, nil
}

func (s *DkgServer) ReceivedRound2Packages(ctx context.Context, req *pb.Round2PackagesRequest) (*pb.Round2PackagesResponse, error) {
	if err := s.state.ReceivedRound2Packages(req.RequestId, s.config.Identifier, req.Round2Packages, req.Round2Signature, &s.frostClient); err != nil {
		return nil, err
	}

	return &pb.Round2PackagesResponse{}, nil
}
