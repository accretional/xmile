// Command xmlserve runs the xmile Documents + Schemas services over gRPC.
package main

import (
	"flag"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
	"github.com/accretional/xmile/service"
)

func main() {
	addr := flag.String("addr", ":50051", "listen address")
	flag.Parse()

	docs, err := service.NewDocumentsServer()
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	g := grpc.NewServer()
	xmlpb.RegisterDocumentsServer(g, docs)
	xmlpb.RegisterSchemasServer(g, service.NewSchemasServer())
	reflection.Register(g) // so grpcurl can call the services without the protos
	log.Printf("xmile Documents + Schemas listening on %s", *addr)
	if err := g.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
