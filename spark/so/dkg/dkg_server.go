package dkg

import (
	"context"
	"log"
	"sync"

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
	log.Println("initiate dkg", req.RequestId, req.MaxSigners)
	if err := s.state.InitiateDkg(req.RequestId, req.MaxSigners); err != nil {
		log.Println("error initiating dkg", err)
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
		log.Println("error in dkg round 1", err)
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	if err := s.state.ProvideRound1Package(req.RequestId, round1Response.Round1Packages); err != nil {
		log.Println("error providing round 1 package", err)
		s.state.RemoveState(req.RequestId)
		return nil, err
	}

	return &pb.InitiateDkgResponse{
		Identifier:    s.config.Identifier,
		Round1Package: round1Response.Round1Packages,
	}, nil
}

func (s *DkgServer) Round1Packages(ctx context.Context, req *pb.Round1PackagesRequest) (*pb.Round1PackagesResponse, error) {
	log.Println("round 1 packages", req.RequestId, req.Round1Packages)
	round1Packages := make([]map[string][]byte, len(req.Round1Packages))
	for i, p := range req.Round1Packages {
		round1Packages[i] = p.Packages
	}

	if err := s.state.ReceivedRound1Packages(req.RequestId, s.config.Identifier, round1Packages); err != nil {
		return nil, err
	}

	signature, err := SignRound1Packages(s.config.IdentityPrivateKey, round1Packages)
	if err != nil {
		return nil, err
	}

	return &pb.Round1PackagesResponse{
		Identifier:      s.config.Identifier,
		Round1Signature: signature,
	}, nil
}

func (s *DkgServer) Round1Signature(ctx context.Context, req *pb.Round1SignatureRequest) (*pb.Round1SignatureResponse, error) {
	log.Println("round 1 signature", req.RequestId, req.Round1Signatures)
	validationFailures, err := s.state.ReceivedRound1Signature(req.RequestId, s.config.Identifier, req.Round1Signatures, s.config.SigningOperatorMap)
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
		delete(p, s.config.Identifier)
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

	var wg sync.WaitGroup
	// Distribute the round 2 package to all participants
	for identifier, operator := range s.config.SigningOperatorMap {
		wg.Add(1)
		go func(identifier string, addr string) {
			defer wg.Done()
			client, err := NewDKGServiceClient(addr)
			if err != nil {
				return
			}

			round2Packages := make([][]byte, len(round2Response.Round2Packages))
			for i, p := range round2Response.Round2Packages {
				round2Packages[i] = p.Packages[identifier]
			}

			round2Signature, err := SignRound2Packages(s.config.IdentityPrivateKey, round2Packages)
			if err != nil {
				return
			}

			go client.Client.Round2Packages(ctx, &pb.Round2PackagesRequest{
				RequestId:       req.RequestId,
				Identifier:      identifier,
				Round2Packages:  round2Packages,
				Round2Signature: round2Signature,
			})
		}(identifier, operator.Address)
	}

	wg.Wait()

	return &pb.Round1SignatureResponse{
		Identifier: s.config.Identifier,
	}, nil
}

func (s *DkgServer) Round2Packages(ctx context.Context, req *pb.Round2PackagesRequest) (*pb.Round2PackagesResponse, error) {
	log.Println("round 2 packages", req.RequestId, req.Round2Packages, req.Round2Signature)
	if err := s.state.ReceivedRound2Packages(req.RequestId, s.config.Identifier, req.Round2Packages, req.Round2Signature, &s.frostClient); err != nil {
		return nil, err
	}

	return &pb.Round2PackagesResponse{}, nil
}
