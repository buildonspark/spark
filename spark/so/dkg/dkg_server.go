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
	state      *DkgStates
	config     *so.Config
}

func NewDkgServer(frostClient frost.FrostClient, config *so.Config) *DkgServer {
	return &DkgServer{
		state:      &DkgStates{},
		frostClient: frostClient,
		config:     config,
	}
}

func (s *DkgServer) InitiateDkg(ctx context.Context, req *pb.InitiateDkgRequest) (*pb.InitiateDkgResponse, error) {
	if err := s.state.InitiateDkg(req.RequestId); err != nil {
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
		return nil, err
	}

	if err := s.state.ProvideRound1Package(req.RequestId, round1Response.Round1Packages); err != nil {
		return nil, err
	}

	return &pb.InitiateDkgResponse{
		Identifier: s.config.Identifier,
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
		Identifier: s.config.Identifier,
		Round1Signature: signature,
	}, nil
}
