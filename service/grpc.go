package service

// grpc.go — the Documents gRPC service: the wire face of Process (ADR 0008).
// Like the Go API it is a classifier — the reply carries the typed AST or a
// verdict, never a non-OK status for a bad document; a non-OK status is reserved
// for a genuine server fault.

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"

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
	if res.Document != nil {
		v := xmlpb.Verdict_WELL_FORMED
		if validating {
			v = xmlpb.Verdict_VALID
		}
		return &xmlpb.ProcessResponse{
			Result:  &xmlpb.ProcessResponse_Document{Document: res.Document},
			Verdict: v,
		}, nil
	}
	tt, terr := typedTree(res.Typed)
	if terr != nil {
		return nil, status.Error(codes.Internal, terr.Error())
	}
	return &xmlpb.ProcessResponse{
		Result:  &xmlpb.ProcessResponse_Typed{Typed: tt},
		Verdict: xmlpb.Verdict_VALID,
	}, nil
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

// typedTree packages a projected message with the descriptor needed to decode
// it, so a dynamic vocabulary's tree is self-describing on the wire.
func typedTree(msg proto.Message) (*xmlpb.TypedTree, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	md := msg.ProtoReflect().Descriptor()
	return &xmlpb.TypedTree{
		Schema:      protodesc.ToFileDescriptorProto(md.ParentFile()),
		RootMessage: string(md.Name()),
		Message:     data,
	}, nil
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
