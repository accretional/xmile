module github.com/accretional/xmile

go 1.25.5

replace github.com/accretional/gluon => ../gluon

replace github.com/accretional/merge => ../proto-merge

require (
	github.com/accretional/gluon v0.0.0-00010101000000-000000000000
	github.com/accretional/merge v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/accretional/proto-expr v0.0.0-20260416071217-9a69001c59bb // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/jhump/protoreflect v1.18.0 // indirect
	github.com/jhump/protoreflect/v2 v2.0.0-beta.1 // indirect
	go.starlark.net v0.0.0-20260522144826-ec58d4b459e2 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516 // indirect
)
