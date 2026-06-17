package service

// schema_grpc.go — the Schemas gRPC service: the wire face of CompileSource, the
// type-level companion to Documents (ADR 0008). It lowers a metagrammar (DTD,
// EBNF vocabulary, or XSD) into a proto descriptor of the document family.

import (
	"context"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// SchemasServer implements the xml.Schemas gRPC service.
type SchemasServer struct {
	xmlpb.UnimplementedSchemasServer
}

// NewSchemasServer builds the schema-compilation service.
func NewSchemasServer() *SchemasServer { return &SchemasServer{} }

// Compile lowers the request's metagrammar into a FileDescriptorProto. A source
// that does not compile rides back as the response's error string (the RPC
// status stays OK).
func (s *SchemasServer) Compile(_ context.Context, req *xmlpb.CompileRequest) (*xmlpb.CompileResponse, error) {
	fdp, err := CompileSource(req.GetSource(), req.GetLanguage(), SchemaOptions{
		Package:   req.GetPackage(),
		GoPackage: req.GetGoPackage(),
		FileName:  req.GetFileName(),
	})
	if err != nil {
		return &xmlpb.CompileResponse{Result: &xmlpb.CompileResponse_Error{Error: err.Error()}}, nil
	}
	return &xmlpb.CompileResponse{Result: &xmlpb.CompileResponse_File{File: fdp}}, nil
}
