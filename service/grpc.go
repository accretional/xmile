package service

// grpc.go — the Documents gRPC service: the wire face of Process (ADR 0008).
// Like the Go API it is a classifier — the reply carries the typed AST or a
// verdict, never a non-OK status for a bad document; a non-OK status is reserved
// for a genuine server fault.

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// DocumentsServer implements the xml.Documents gRPC service. The service is the
// parser: Process delegates to the grammar-driven Parser.
type DocumentsServer struct {
	xmlpb.UnimplementedDocumentsServer
	parser *Parser
}

// NewDocumentsServer builds a service backed by the default XML 1.0 parser.
func NewDocumentsServer() (*DocumentsServer, error) {
	p, err := Default()
	if err != nil {
		return nil, err
	}
	return &DocumentsServer{parser: p}, nil
}

// Process parses the request bytes and, given a schema, projects them into that
// vocabulary's typed tree; with no schema it returns the generic XML AST.
func (s *DocumentsServer) Process(_ context.Context, req *xmlpb.ProcessRequest) (*xmlpb.ProcessResponse, error) {
	validating := req.GetMode() == xmlpb.Mode_VALIDATE
	schema, err := s.resolveSchema(req)
	if err != nil {
		return processErr(xmlpb.Verdict_INVALID, err.Error()), nil
	}
	res, perr := s.parser.Process(string(req.GetSource()), schema, validating)
	if perr != nil {
		v := verdictOf(perr)
		return processErr(v, perr.Error()), nil
	}
	inner, terr := projectedStruct(res.Document)
	if terr != nil {
		return nil, status.Error(codes.Internal, terr.Error())
	}
	// One shape for every input: the result keyed by its vocabulary — "xml" for a
	// generic parse, the root element (e.g. "rss") for a projected format.
	key := res.Root
	verdict := xmlpb.Verdict_VALID
	if key == "" { // generic XML (no schema)
		key = "xml"
		if !validating {
			verdict = xmlpb.Verdict_WELL_FORMED
		}
	}
	doc := &structpb.Struct{Fields: map[string]*structpb.Value{
		key: structpb.NewStructValue(inner),
	}}
	return &xmlpb.ProcessResponse{
		Result:  &xmlpb.ProcessResponse_Document{Document: doc},
		Verdict: verdict,
	}, nil
}

// Generate serializes the generic XML AST in the request back to a document. The
// request carries the typed Xml tree directly (not a Struct), so a format's typed
// projection — a read-only view that may have dropped unmodeled markup — cannot
// be passed: only the lossless generic AST round-trips.
func (s *DocumentsServer) Generate(_ context.Context, req *xmlpb.GenerateRequest) (*xmlpb.GenerateResponse, error) {
	out, err := Generate(req.GetDocument())
	if err != nil {
		return generateErr(err.Error()), nil
	}
	return &xmlpb.GenerateResponse{Result: &xmlpb.GenerateResponse_Source{Source: out}}, nil
}

func generateErr(reason string) *xmlpb.GenerateResponse {
	return &xmlpb.GenerateResponse{
		Result: &xmlpb.GenerateResponse_Error{Error: &xmlpb.GenerateError{Reason: reason}},
	}
}

// resolveSchema turns the request's schema selector into a *Schema: a registered
// format, an inline metagrammar compile, or nil (generic XML).
func (s *DocumentsServer) resolveSchema(req *xmlpb.ProcessRequest) (*Schema, error) {
	switch sel := req.GetSchema().(type) {
	case *xmlpb.ProcessRequest_Format:
		return Format(sel.Format)
	case *xmlpb.ProcessRequest_Compile:
		c := sel.Compile
		fdp, err := CompileSource(c.GetSource(), c.GetLanguage(), SchemaOptions{
			Package:   c.GetPackage(),
			GoPackage: c.GetGoPackage(),
			FileName:  c.GetFileName(),
		})
		if err != nil {
			return nil, err
		}
		return schemaFromCompiled(fdp, false)
	}
	return nil, nil
}

// projectedStruct renders a projected message as a google.protobuf.Struct via
// its JSON form, so the reply carries the readable AST. The caller keys it under
// the root element name. The schema (the family's descriptor) is
// Schemas.Compile's job, not repeated here.
func projectedStruct(msg proto.Message) (*structpb.Struct, error) {
	js, err := protojson.Marshal(msg)
	if err != nil {
		return nil, err
	}
	st := &structpb.Struct{}
	if err := protojson.Unmarshal(js, st); err != nil {
		return nil, err
	}
	return st, nil
}

func processErr(v xmlpb.Verdict, reason string) *xmlpb.ProcessResponse {
	return &xmlpb.ProcessResponse{
		Result:  &xmlpb.ProcessResponse_Error{Error: &xmlpb.ProcessError{Verdict: v, Reason: reason}},
		Verdict: v,
	}
}

// verdictOf classifies a parser rejection into the wire Verdict.
func verdictOf(err error) xmlpb.Verdict {
	switch err.(type) {
	case *ValidityError:
		return xmlpb.Verdict_INVALID
	case *CannotValidateError:
		return xmlpb.Verdict_CANNOT_VALIDATE
	default:
		return xmlpb.Verdict_NOT_WELL_FORMED
	}
}
