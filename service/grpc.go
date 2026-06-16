package service

import (
	"context"

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
