package service

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// SchemaServer implements the xml.SchemaService gRPC service: it compiles a
// DTD into a proto descriptor (see ADR 0004). It is the type-level companion
// to Server (which serves XmlService.Parse).
type SchemaServer struct {
	xmlpb.UnimplementedSchemaServiceServer
}

// NewSchemaServer builds the schema-compilation service.
func NewSchemaServer() *SchemaServer { return &SchemaServer{} }

// Compile lowers the request's DTD bytes into a FileDescriptorProto. A DTD
// that is not well-formed returns INVALID_ARGUMENT with no descriptor.
func (s *SchemaServer) Compile(_ context.Context, req *xmlpb.CompileRequest) (*xmlpb.CompileResponse, error) {
	fdp, err := CompileDTD(req.GetDtd(), SchemaOptions{
		Package:   req.GetPackage(),
		GoPackage: req.GetGoPackage(),
		FileName:  req.GetFileName(),
	})
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return &xmlpb.CompileResponse{File: fdp}, nil
}
