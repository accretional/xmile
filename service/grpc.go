package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// Server implements the xml.XmlService gRPC service. The service is the
// parser: Parse delegates to the grammar-driven Parser.
type Server struct {
	xmlpb.UnimplementedXmlServiceServer
	parser *Parser
}

// NewServer builds a service backed by the default XML 1.0 parser.
func NewServer() (*Server, error) {
	p, err := Default()
	if err != nil {
		return nil, err
	}
	return &Server{parser: p}, nil
}

// Parse parses the request bytes into a Document AST. Not-well-formed input
// returns INVALID_ARGUMENT with no tree; a well-formed document that violates
// its DTD returns FAILED_PRECONDITION. Both carry no tree.
func (s *Server) Parse(_ context.Context, req *xmlpb.ParseRequest) (*xmlpb.ParseResponse, error) {
	doc, err := s.parser.Parse(string(req.GetXml()))
	if err != nil {
		if _, ok := err.(*ValidityError); ok {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &xmlpb.ParseResponse{Document: doc}, nil
}
