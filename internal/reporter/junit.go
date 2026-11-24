package reporter

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// JUnit XML structures per the standard JUnit schema.
type junitTestsuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	Testsuites []junitTestsuite `xml:"testsuite"`
}

type junitTestsuite struct {
	Name       string           `xml:"name,attr"`
	Tests      int              `xml:"tests,attr"`
	Failures   int              `xml:"failures,attr"`
	Time       string           `xml:"time,attr"`
	Properties *junitProperties `xml:"properties,omitempty"`
	Cases      []junitTestcase  `xml:"testcase"`
}

// junitProperties is JUnit XML's conventional home for non-fatal per-suite
// information the schema has no native field for — the transport
// substitution warning has no dedicated element to
// live in.
type junitProperties struct {
	Properties []junitProperty `xml:"property"`
}

type junitProperty struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type junitTestcase struct {
	Name       string           `xml:"name,attr"`
	Classname  string           `xml:"classname,attr"`
	Time       string           `xml:"time,attr"`
	Properties *junitProperties `xml:"properties,omitempty"`
	Failure    *junitFailure    `xml:"failure,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// JUnitReporter produces standard JUnit XML output.
type JUnitReporter struct {
	// NoMask disables E.164 masking (see NewPassthroughMasker).
	NoMask bool
}

// Report writes JUnit XML for all scenario results.
func (j *JUnitReporter) Report(w io.Writer, results []*engine.RunResult) error {
	suites := junitTestsuites{}
	masker := maskerFor(j.NoMask, results)

	for _, r := range results {
		suites.Testsuites = append(suites.Testsuites, buildJUnitTestsuite(r, masker))
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(suites); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// buildJUnitTestsuite converts one RunResult into a junitTestsuite, extracted
// from Report per CLAUDE.md's 40-line cap.
func buildJUnitTestsuite(r *engine.RunResult, masker *Masker) junitTestsuite {
	suite := junitTestsuite{
		Name:       r.ScenarioName,
		Tests:      len(r.Steps),
		Time:       fmt.Sprintf("%.3f", float64(r.DurationMs)/1000.0),
		Properties: buildJUnitProperties(r.Warnings, masker),
	}

	for _, step := range r.Steps {
		tc := buildJUnitTestcase(step, masker)
		if tc.Failure != nil {
			suite.Failures++
		}
		suite.Cases = append(suite.Cases, tc)
	}

	return suite
}

// buildJUnitProperties builds the <properties> block carrying run-level
// warnings, or nil when there are none. A nil pointer with `omitempty` is
// what encoding/xml omits — this is what keeps docs/examples/junit.xml
// byte-identical for a run with no warnings.
func buildJUnitProperties(warnings []string, masker *Masker) *junitProperties {
	if len(warnings) == 0 {
		return nil
	}
	props := &junitProperties{}
	for _, warning := range warnings {
		props.Properties = append(props.Properties, junitProperty{
			Name:  "cellrift.warning",
			Value: masker.Mask(warning),
		})
	}
	return props
}

// buildJUnitTestcase converts one StepReport into a junitTestcase.
func buildJUnitTestcase(step engine.StepReport, masker *Masker) junitTestcase {
	tc := junitTestcase{
		Name:      step.StepID,
		Classname: fmt.Sprintf("%s.%s", step.Protocol, step.Action),
		Time:      fmt.Sprintf("%.3f", float64(step.Result.DurationMs)/1000.0),
	}

	// JUnit's schema has no element for a per-testcase numeric fact, the same
	// gap buildJUnitProperties documents at suite level for warnings — a
	// property is the conventional home for it.
	if ms, ok := impairDelayMs(step.Result); ok {
		tc.Properties = &junitProperties{Properties: []junitProperty{{
			Name:  "cellrift.impair_delay_ms",
			Value: strconv.FormatInt(ms, 10),
		}}}
	}

	if step.Result.Status == common.StatusFailed {
		// Same precedence as terminal.go and json.go: Result.Error wins,
		// StepReport.Error is the fallback. This copy matters most of the
		// three: JUnit is what a CI system reads, so the inverted order
		// produced a red build whose failure message named an assertion
		// instead of the certificate the peer rejected.
		msg := masker.Mask(step.Result.Error)
		if step.Result.Error == "" && step.Error != nil {
			msg = masker.Mask(step.Error.Error())
		}
		tc.Failure = &junitFailure{Message: msg, Body: msg}
	}

	return tc
}
