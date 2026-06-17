package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

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

// Parse parses the request bytes and returns either the Document or a typed
// ParseError verdict (see ADR 0006). The RPC status is OK in both cases; a
// non-OK status is reserved for a genuine server fault.
func (s *Server) Parse(_ context.Context, req *xmlpb.ParseRequest) (*xmlpb.ParseResponse, error) {
	doc, err := s.parser.Parse(string(req.GetXml()), req.GetValidate())
	if err != nil {
		return &xmlpb.ParseResponse{Response: &xmlpb.ParseResponse_Error{Error: verdictOf(err)}}, nil
	}
	return &xmlpb.ParseResponse{Response: &xmlpb.ParseResponse_Document{Document: doc}}, nil
}

// ParseRss parses RSS 2.0 bytes and returns either the typed RSS AST (an
// rss.Rss carried in an Any) or a typed ParseError verdict. As with Parse the
// RPC status is OK for any well-formed-or-not document; a non-OK status is
// reserved for a genuine server fault (e.g. the embedded grammar fails to
// compile, or the AST cannot be packed).
func (s *Server) ParseRss(_ context.Context, req *xmlpb.ParseRssRequest) (*xmlpb.ParseRssResponse, error) {
	msg, err := ParseRSS(s.parser, string(req.GetXml()))
	if err != nil {
		switch err.(type) {
		case *WFError, *ValidityError, *CannotValidateError:
			return &xmlpb.ParseRssResponse{Response: &xmlpb.ParseRssResponse_Error{Error: verdictOf(err)}}, nil
		default:
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	packed, err := anypb.New(msg)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &xmlpb.ParseRssResponse{Response: &xmlpb.ParseRssResponse_Rss{Rss: packed}}, nil
}

// verdictOf classifies a parser rejection into the wire ParseError.
func verdictOf(err error) *xmlpb.ParseError {
	v := xmlpb.Verdict_NOT_WELL_FORMED
	switch err.(type) {
	case *ValidityError:
		v = xmlpb.Verdict_INVALID
	case *CannotValidateError:
		v = xmlpb.Verdict_CANNOT_VALIDATE
	}
	return &xmlpb.ParseError{Verdict: v, Reason: err.Error()}
}
