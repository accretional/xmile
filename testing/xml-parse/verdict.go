package main

import "github.com/accretional/xmile/service"

// verdict is the harness's three-way outcome, mirroring the W3C categories.
// A well-formed document with no (readable) DTD is "valid": there is nothing
// to invalidate it.
type verdict string

const (
	notWF   verdict = "not-wf"
	invalid verdict = "invalid"
	valid   verdict = "valid"
)

// validate parses src and classifies the outcome by the parser's error type:
// no error -> valid, *service.ValidityError -> invalid, anything else -> not-wf.
func validate(src string) (verdict, error) {
	p, err := service.Default()
	if err != nil {
		return notWF, err
	}
	_, perr := p.Parse(src)
	switch perr.(type) {
	case nil:
		return valid, nil
	case *service.ValidityError:
		return invalid, perr
	default:
		return notWF, perr
	}
}
