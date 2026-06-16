package main

import "github.com/accretional/xmile/service"

// verdict is the corpus harness's coarse outcome. DTD validity is not yet
// distinguished from well-formedness, so a well-formed document reports
// wellFormed regardless of any DTD.
type verdict string

const (
	notWF      verdict = "not-wf"
	wellFormed verdict = "well-formed"
)

// validate parses src with the service parser.
func validate(src string) (verdict, error) {
	p, err := service.Default()
	if err != nil {
		return notWF, err
	}
	if _, perr := p.Parse(src); perr != nil {
		return notWF, perr
	}
	return wellFormed, nil
}
