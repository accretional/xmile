// Command xmlserve runs the xmile XmlService over gRPC.
package main

import (
	"flag"
	"log"
	"net"

	"google.golang.org/grpc"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
	"github.com/accretional/xmile/service"
)

func main() {
	addr := flag.String("addr", ":50051", "listen address")
	flag.Parse()

	srv, err := service.NewServer()
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	g := grpc.NewServer()
	xmlpb.RegisterXmlServiceServer(g, srv)
	xmlpb.RegisterSchemaServiceServer(g, service.NewSchemaServer())
	log.Printf("xmile XmlService + SchemaService listening on %s", *addr)
	if err := g.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
