package service

import "google.golang.org/protobuf/reflect/protoreflect"

// stringField returns the string value of the named field of m, or "" when the
// field is absent — a small reflection helper shared by the vocabulary
// projection tests (docx_test.go, xlsx_test.go).
func stringField(m protoreflect.Message, name string) string {
	f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if f == nil {
		return ""
	}
	return m.Get(f).String()
}
