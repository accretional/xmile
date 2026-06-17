package service

// engine.go — the generic, schema-driven projection walk. One walk over a
// parsed Tag tree against a compiled schema descriptor projects the tree into a
// typed message; the same naming convention serves every vocabulary (RSS, a
// DTD-compiled schema, a future XSD/OOXML schema). It is the single engine that
// subsumes the former projectRSS and ProjectTag — the "generic XML AST + walk
// against a vocabulary" step of ADR 0008, applied to any format.
//
// The companion concern, validation, stays a separate pass: projection is the
// loosest reading (place what fits, report what does not), while validity
// (content models, attribute VCs, ID/IDREF) is checked by validate.go in
// validating mode. The two share this Tag tree but not this code.

import (
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	xmlpb "github.com/accretional/xmile/proto/pb/xml"
)

// projectOptions tune the generic walk for one vocabulary's extensibility rule.
type projectOptions struct {
	// nsExtensible tolerates namespace-qualified markup as a foreign extension
	// (the RSS 2.0 rule: any element/attribute is allowed "only if defined in a
	// namespace") instead of reporting it as out-of-vocabulary. When false, a
	// namespaced child/attribute with no matching field is reported as unknown
	// like any other — the right default for a closed vocabulary (a DTD).
	nsExtensible bool
}

// project walks a parsed Tag tree against a schema descriptor, filling msg.
// Attributes map to string fields by snake-cased name; a leaf element's text
// maps to its `text` field; a child element maps to a direct message field named
// after it or to a oneof variant inside a (possibly repeated) wrapper message —
// all by placeChild. It returns the names of tolerated namespace-qualified
// extensions (foreign) and of markup with no matching field (unknown). xmlns
// declarations are never data.
func project(tag *xmlpb.Tag, md protoreflect.MessageDescriptor, msg protoreflect.Message, opts projectOptions) (foreign, unknown []string) {
	for _, a := range tag.GetAttrs() {
		name := a.GetName()
		switch {
		case name == "xmlns" || strings.HasPrefix(name, "xmlns:"):
			continue // a namespace declaration, not data
		case opts.nsExtensible && a.GetNamespace().GetNamespaceUri() != "":
			foreign = append(foreign, "@"+name) // namespaced extension attribute
		default:
			if f := scalarField(md, name); f != nil {
				msg.Set(f, protoreflect.ValueOfString(a.GetValue()))
			} else {
				unknown = append(unknown, tag.GetName()+"@"+name)
			}
		}
	}

	// A leaf element (a #PCDATA element such as <title>) holds character content,
	// which may carry inline markup — entity-encoded or raw. That markup is the
	// element's text, not vocabulary, so a leaf's value is all of its descendant
	// character data and we do not recurse into it.
	if tf := textField(md); tf != nil && !hasMessageField(md) {
		if s := gatherText(tag); strings.TrimSpace(s) != "" {
			msg.Set(tf, protoreflect.ValueOfString(s))
		}
		return foreign, unknown
	}

	for _, ci := range tag.GetContents() {
		child := ci.GetChild()
		if child == nil {
			continue // text/cdata between child elements is insignificant here
		}
		if opts.nsExtensible && child.GetNamespace().GetNamespaceUri() != "" {
			foreign = append(foreign, child.GetName()) // namespaced extension element
			continue
		}
		cd, cm, ok := placeChild(md, msg, child.GetName())
		if !ok {
			unknown = append(unknown, tag.GetName()+">"+child.GetName())
			continue
		}
		f, u := project(child, cd, cm, opts)
		foreign = append(foreign, f...)
		unknown = append(unknown, u...)
	}
	return foreign, unknown
}

// textField returns md's string `text` field (a leaf's character content), or
// nil. hasMessageField reports whether md has any message-typed field (i.e. it
// nests child elements); a message with a text field and no message fields is a
// #PCDATA leaf.
func textField(md protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	if f := md.Fields().ByName("text"); f != nil && f.Kind() == protoreflect.StringKind {
		return f
	}
	return nil
}

func hasMessageField(md protoreflect.MessageDescriptor) bool {
	fs := md.Fields()
	for i := 0; i < fs.Len(); i++ {
		if fs.Get(i).Kind() == protoreflect.MessageKind {
			return true
		}
	}
	return false
}

// gatherText concatenates all descendant character data (text and CDATA) of tag,
// in document order — the textual value of a #PCDATA element, including any
// inline markup folded back to its text.
func gatherText(tag *xmlpb.Tag) string {
	var b strings.Builder
	var walk func(*xmlpb.Tag)
	walk = func(t *xmlpb.Tag) {
		for _, ci := range t.GetContents() {
			switch it := ci.GetItem().(type) {
			case *xmlpb.ContentItem_Text:
				b.WriteString(it.Text)
			case *xmlpb.ContentItem_Cdata:
				b.WriteString(it.Cdata)
			case *xmlpb.ContentItem_Child:
				walk(it.Child)
			}
		}
	}
	walk(tag)
	return b.String()
}
