package main

import "github.com/accretional/xmile/service"

// verdict is the harness's outcome, mirroring the W3C categories plus the
// "cannot validate" outcome of validating mode.
type verdict string

const (
	notWF          verdict = "not-wf"
	invalid        verdict = "invalid"
	valid          verdict = "valid"
	cannotValidate verdict = "cannot-validate"
)

// validate parses src in the given mode and classifies the outcome by the
// parser's error type. In non-validating mode a well-formed document is
// "valid" (there is no validity claim); in validating mode it is "valid" only
// if it also has a DTD and satisfies it.
func validate(src string, validating bool) (verdict, error) {
	p, err := service.Default()
	if err != nil {
		return notWF, err
	}
	_, perr := p.Parse(src, validating)
	switch perr.(type) {
	case nil:
		return valid, nil
	case *service.ValidityError:
		return invalid, perr
	case *service.CannotValidateError:
		return cannotValidate, perr
	default:
		return notWF, perr
	}
}
