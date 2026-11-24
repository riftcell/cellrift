package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/riftcell/cellrift/internal/reporter"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/riftcell/cellrift/scenarios"
	"github.com/spf13/cobra"
)

var version = "0.1.0-dev"

// Exit codes.
const (
	exitOK             = 0
	exitTestFailure    = 1
	exitExecutionError = 2
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		if exitErr, ok := err.(*exitError); ok {
			// An exitError's message was already printed at the point of
			// failure; only the code is needed here.
			os.Exit(exitErr.code)
		}
		// Anything else — an unknown flag, a bad argument count — comes from
		// cobra, whose own printing is silenced on every subcommand. Without
		// this line `cellrift validate --strict x` exited 1 with no output.
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(exitTestFailure)
	}
}

// exitError carries a specific exit code.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "cellrift",
		Short:   "CI/CD-native multi-protocol Telecom testing framework",
		Long:    "Cellrift — write once, run anywhere, integrate everywhere.\nBuilt to find where cellular systems crack under pressure.",
		Version: version,
	}

	root.AddCommand(runCmd(), validateCmd(), replayCmd(), listCmd(), assertCmd(), initCmd(), selftestCmd())
	return root
}

// runConfig holds the resolved flag values for the run command.
type runConfig struct {
	targetHost string
	vars       []string
	format     string
	output     string
	timeout    time.Duration
	failFast   bool
	verbose    bool
	parallel   bool
	noMask     bool
}

func runCmd() *cobra.Command {
	var (
		targetHost string
		vars       []string
		format     string
		output     string
		timeout    int
		failFast   bool
		verbose    bool
		parallel   bool
		noMask     bool
	)

	cmd := &cobra.Command{
		Use:           "run <scenario.yaml> [scenario2.yaml ...]",
		Short:         "Execute scenario files against a target environment",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := &runConfig{
				targetHost: targetHost,
				vars:       vars,
				format:     format,
				output:     output,
				timeout:    time.Duration(timeout) * time.Millisecond,
				failFast:   failFast,
				verbose:    verbose,
				parallel:   parallel,
				noMask:     noMask,
			}
			return cfg.executeRun(args)
		},
	}

	cmd.Flags().StringVar(&targetHost, "target-host", "", "Override target host for all steps")
	cmd.Flags().StringArrayVar(&vars, "var", nil, "Override scenario variable, e.g. key=value (repeatable)")
	cmd.Flags().StringVar(&format, "format", "terminal", "Output format: terminal, junit, json")
	cmd.Flags().StringVar(&output, "output", "", "Write output to file instead of stdout")
	cmd.Flags().IntVar(&timeout, "timeout", 3000, "Global step timeout in ms")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Stop on first assertion failure")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Print all protocol messages")
	cmd.Flags().BoolVar(&parallel, "parallel", false, "Run multiple scenario files in parallel")
	cmd.Flags().BoolVar(&noMask, "no-mask", false, "Do not mask phone numbers in output (lab use only; applies to every format). Credentials are removed regardless of this flag.")

	return cmd
}

// executeRun is the top-level body of the run command.
func (cfg *runConfig) executeRun(args []string) error {
	results, executionError := collectResults(args, cfg.parallel, cfg.runScenario)
	return cfg.reportAndExit(results, executionError)
}

// reportAndExit writes results through the selected reporter and converts
// them to the process exit code. Shared by run and selftest so the two
// commands cannot drift in how a result is rendered or judged.
func (cfg *runConfig) reportAndExit(results []*engine.RunResult, executionError bool) error {
	w, closeOutput, err := openOutputWriter(cfg.output)
	if err != nil {
		return &exitError{code: exitExecutionError, msg: err.Error()}
	}
	defer closeOutput()

	rep := selectReporter(cfg.format, w, cfg.verbose, cfg.noMask)
	if len(results) > 0 {
		if err := rep.Report(w, results); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: writing report: %s\n", err)
			return &exitError{code: exitExecutionError, msg: err.Error()}
		}
	}

	return determineExitCode(results, executionError)
}

// runScenario parses, validates, builds adapters for, and executes one scenario file.
// Returns nil on any error (the error has already been printed to stderr).
func (cfg *runConfig) runScenario(path string) *engine.RunResult {
	s, err := parseAndValidate(path, cfg.targetHost)
	if err != nil {
		return nil
	}
	return cfg.executeParsed(s)
}

// executeParsed builds adapters for an already-validated scenario and runs
// it. Returns nil if the adapters cannot be built (already printed).
func (cfg *runConfig) executeParsed(s *scenario.Scenario) *engine.RunResult {
	adapters, warnings, cleanup, err := buildAdapters(s, cfg.timeout)
	if err != nil {
		return nil
	}
	defer cleanup()

	opts := engine.RunOptions{
		FailFast:     cfg.failFast,
		Timeout:      cfg.timeout,
		VarOverrides: cfg.vars,
		Environ:      os.Environ(),
		Warnings:     warnings,
	}
	return engine.Run(s, adapters, opts)
}

// parseAndValidate parses a scenario file, validates it, and applies the
// --target-host override. Returns a non-nil error if the file cannot be used.
func parseAndValidate(path, targetHost string) (*scenario.Scenario, error) {
	s, err := scenario.ParseFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		return nil, err
	}

	if errs := scenario.Validate(s); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "%s: %s\n", path, e.Error())
		}
		return nil, fmt.Errorf("validation failed")
	}

	if targetHost != "" {
		applyTargetHostOverride(s.Steps, targetHost)
	}
	return s, nil
}

// applyTargetHostOverride sets Target on every step in steps AND recurses
// into each step's own Steps — the sub-steps of a parallel or reorder
// group. Without the recursion, --target-host silently overrode only
// top-level steps: a reorder or parallel group's sub-steps kept whatever
// target: the YAML declared, so a scenario author using --target-host
// against a reorder group would see it connect to the wrong host with no
// error anywhere, the same "validated but not actually wired" defect class
// the end-to-end tests exist to catch elsewhere.
func applyTargetHostOverride(steps []scenario.Step, targetHost string) {
	for i := range steps {
		steps[i].Target = targetHost
		if len(steps[i].Steps) > 0 {
			applyTargetHostOverride(steps[i].Steps, targetHost)
		}
	}
}

// buildSIPAdapter creates the SIP adapter, using TLS transport when the
// protocol config specifies transport: tls.
//
// The transport is built here rather than inside sip.NewAdapter so it can be
// decorated before the adapter ever sees it: every scenario step may declare
// an impair: block, and impair.Wrap is what makes that possible. Wrapping is
// unconditional and costs nothing — the decorator is a pass-through until a
// step actually arms it, so a scenario that declares no impairment behaves
// exactly as it did before.
//
// The returned warnings mirror buildDiameterAdapter's: run-level observations
// destined for engine.RunOptions.Warnings. Today the only one is the
// insecure_skip_verify opt-in, so a run that deliberately disabled server
// verification says so in every reporter rather than only in a source comment.
func buildSIPAdapter(cfg *scenario.ProtocolConfig, timeout time.Duration) (*sip.Adapter, []string, error) {
	transport := cfg.Transport
	if transport == "" {
		transport = "udp"
	}
	if transport != "tls" {
		tr, err := common.NewTransport(transport, ":0")
		if err != nil {
			return nil, nil, fmt.Errorf("sip adapter: %w", err)
		}
		return sip.NewAdapterWithTransport(impair.Wrap(tr), transport, timeout), nil, nil
	}
	tlsCfg, err := common.BuildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.InsecureSkipVerify)
	if err != nil {
		return nil, nil, fmt.Errorf("TLS config: %w", err)
	}
	tr, err := common.NewTLSTransport(":0", tlsCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("sip adapter tls: %w", err)
	}
	var warnings []string
	if cfg.InsecureSkipVerify {
		warnings = append(warnings, insecureTLSWarning)
	}
	return sip.NewAdapterWithTransport(impair.Wrap(tr), "tls", timeout), warnings, nil
}

// insecureTLSWarning is the run-level warning emitted when a scenario opts
// out of TLS server verification. It names the key so a reader of a CI log
// can find the line that caused it.
const insecureTLSWarning = "sip: insecure_skip_verify is set — the TLS server certificate is NOT verified; never use this against production infrastructure"

// buildDiameterAdapter creates the Diameter adapter, honouring the transport
// the scenario declares.
//
// transport: tcp means TCP and only TCP — no SCTP attempt, and therefore no
// substitution warning, because nothing was substituted. transport: sctp (or
// an unset value, which defaults to sctp for Diameter) attempts a real SCTP
// association and falls back to TCP if the platform cannot provide one,
// returning a warning that says so.
//
// Both branches build the transport here and wrap it with impair.Wrap before
// handing it to the adapter, so every scenario step can declare an impair:
// block. diameter.NewAdapter is deliberately not used from production any
// more: it builds its transport internally and offers no seam to decorate it.
func buildDiameterAdapter(cfg *scenario.ProtocolConfig, timeout time.Duration) (*diameter.Adapter, []string, error) {
	if cfg != nil && cfg.Transport == "tcp" {
		tr, err := common.NewTCPTransport(":0")
		if err != nil {
			return nil, nil, fmt.Errorf("diameter tcp transport: %w", err)
		}
		return diameter.NewAdapterWithTransport(impair.Wrap(tr), timeout), nil, nil
	}
	tr, warnings, err := common.NewSCTPOrFallbackTransport(":0")
	if err != nil {
		return nil, nil, fmt.Errorf("diameter adapter: %w", err)
	}
	return diameter.NewAdapterWithTransport(impair.Wrap(tr), timeout), warnings, nil
}

// addDiameterAdapter builds and registers the Diameter adapter into adapters,
// appending its transport-substitution warning (if any) to warnings and its
// Close to closers. Extracted from buildAdapters to keep that function under
// CLAUDE.md's 40-line cap once warning accumulation was added.
func addDiameterAdapter(s *scenario.Scenario, timeout time.Duration, adapters common.AdapterMap, warnings *[]string, closers *[]func()) error {
	a, w, err := buildDiameterAdapter(s.Protocols["diameter"], timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: create Diameter adapter: %s\n", err)
		return err
	}
	adapters["diameter"] = a
	*warnings = append(*warnings, w...)
	*closers = append(*closers, func() { a.Close() })
	return nil
}

// buildAdapters creates protocol adapters for every protocol declared in s.
// The returned cleanup function closes all adapters and must be called by the
// caller. The returned warnings are run-level observations (currently only a
// Diameter transport substitution) gathered at construction time, destined
// for engine.RunOptions.Warnings.
func buildAdapters(s *scenario.Scenario, timeout time.Duration) (common.AdapterMap, []string, func(), error) {
	adapters := make(common.AdapterMap)
	var closers []func()
	var warnings []string
	cleanup := func() {
		for _, c := range closers {
			c()
		}
	}

	if sipCfg, ok := s.Protocols["sip"]; ok {
		a, w, err := buildSIPAdapter(sipCfg, timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: create SIP adapter: %s\n", err)
			return nil, nil, nil, err
		}
		adapters["sip"] = a
		warnings = append(warnings, w...)
		closers = append(closers, func() { a.Close() })
	}

	if _, ok := s.Protocols["diameter"]; ok {
		if err := addDiameterAdapter(s, timeout, adapters, &warnings, &closers); err != nil {
			cleanup()
			return nil, nil, nil, err
		}
	}

	return adapters, warnings, cleanup, nil
}

// collectResults runs scenarios sequentially or in parallel and returns the
// results plus a flag indicating whether any scenario failed to execute at all.
func collectResults(args []string, parallel bool, runOne func(string) *engine.RunResult) ([]*engine.RunResult, bool) {
	if parallel && len(args) > 1 {
		return collectParallel(args, runOne)
	}
	return collectSequential(args, runOne)
}

func collectSequential(args []string, runOne func(string) *engine.RunResult) ([]*engine.RunResult, bool) {
	raw := make([]*engine.RunResult, len(args))
	for i, path := range args {
		raw[i] = runOne(path)
	}
	return filterNil(raw)
}

func collectParallel(args []string, runOne func(string) *engine.RunResult) ([]*engine.RunResult, bool) {
	raw := make([]*engine.RunResult, len(args))
	var wg sync.WaitGroup
	for i, path := range args {
		wg.Add(1)
		go func(idx int, p string) {
			defer wg.Done()
			raw[idx] = runOne(p)
		}(i, path)
	}
	wg.Wait()
	return filterNil(raw)
}

// filterNil separates non-nil results from nil slots (execution errors).
func filterNil(raw []*engine.RunResult) ([]*engine.RunResult, bool) {
	var results []*engine.RunResult
	executionError := false
	for _, r := range raw {
		if r != nil {
			results = append(results, r)
		} else {
			executionError = true
		}
	}
	return results, executionError
}

// openOutputWriter returns a writer for the given path, or stdout if path is empty.
// The caller must invoke the returned close function when done.
func openOutputWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot write to %s: %s\n", path, err)
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

// selectReporter returns the reporter matching the format flag.
func selectReporter(format string, w io.Writer, verbose, noMask bool) reporter.Reporter {
	switch format {
	case "junit":
		return &reporter.JUnitReporter{NoMask: noMask}
	case "json":
		return &reporter.JSONReporter{NoMask: noMask}
	default:
		t := reporter.NewTerminalReporter(w, verbose)
		t.NoMask = noMask
		return t
	}
}

// determineExitCode maps results and execution errors to an exitError or nil.
func determineExitCode(results []*engine.RunResult, executionError bool) error {
	if executionError && len(results) == 0 {
		return &exitError{code: exitExecutionError, msg: "execution error"}
	}
	for _, r := range results {
		if r.Status == common.StatusFailed {
			return &exitError{code: exitTestFailure, msg: "one or more scenarios failed"}
		}
	}
	if executionError {
		return &exitError{code: exitExecutionError, msg: "execution error"}
	}
	return nil
}

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "validate <scenario.yaml> [scenario2.yaml ...]",
		Short:         "Validate scenario file syntax and schema without executing",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			hasErrors := false
			for _, path := range args {
				s, err := scenario.ParseFile(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s\n", err)
					hasErrors = true
					continue
				}

				errs := scenario.Validate(s)
				for _, e := range errs {
					fmt.Fprintf(os.Stderr, "%s: %s\n", path, e.Error())
				}
				if len(errs) > 0 {
					hasErrors = true
					continue
				}
				// Say so on success. A silent exit 0 was indistinguishable from
				// "nothing ran" — and it is the first command in the quickstart.
				fmt.Fprintf(os.Stdout, "%s: OK\n", path)
			}

			if hasErrors {
				return fmt.Errorf("validation failed")
			}
			return nil
		},
	}

	// No --strict flag: it was declared, documented, and never read. Strict
	// behaviour is now the only behaviour — unknown keys, absent assertion
	// operands, and undeclared protocols are all rejected unconditionally —
	// so a flag promising it would only invite the assumption that its
	// absence meant something looser.

	return cmd
}

func replayCmd() *cobra.Command {
	var (
		protocol       string
		exportScenario bool
		filter         string
	)

	cmd := &cobra.Command{
		Use:           "replay <capture.pcap>",
		Short:         "Replay a Wireshark PCAP capture as an executable test scenario (not yet implemented)",
		Args:          cobra.ExactArgs(1),
		Hidden:        true, // a stub; hidden from --help so it is not mistaken for a shipped feature
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Non-zero, not exit 0: a CI step that invoked this used to
			// succeed silently while doing nothing at all.
			fmt.Fprintln(os.Stderr, "ERROR: cellrift replay is not yet implemented")
			return &exitError{code: exitExecutionError, msg: "replay is not yet implemented"}
		},
	}

	cmd.Flags().StringVar(&protocol, "protocol", "", "Protocol to extract: sip, diameter, gtp")
	cmd.Flags().BoolVar(&exportScenario, "export-scenario", false, "Write generated scenario YAML to stdout")
	cmd.Flags().StringVar(&filter, "filter", "", "BPF filter to apply before extraction")

	return cmd
}

func assertCmd() *cobra.Command {
	var input string

	cmd := &cobra.Command{
		Use:           "assert [flags] <expression> [expression ...]",
		Short:         "Standalone assertion runner for CI pipeline steps",
		Long:          "Evaluate assertions against a JSON or YAML input without running a full scenario.\nReads input data from --input file, then checks each assertion expression.",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load input data.
			data := make(map[string]interface{})
			if input != "" {
				raw, err := os.ReadFile(input)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ERROR: read input: %s\n", err)
					return &exitError{code: exitExecutionError, msg: fmt.Sprintf("read input: %s", err)}
				}

				// Try JSON first, then YAML.
				if err := json.Unmarshal(raw, &data); err != nil {
					// Fall back to YAML.
					yamlData := make(map[string]interface{})
					if yerr := yamlUnmarshal(raw, &yamlData); yerr != nil {
						fmt.Fprintf(os.Stderr, "ERROR: parse input: not valid JSON or YAML\n")
						return &exitError{code: exitExecutionError, msg: "parse input: not valid JSON or YAML"}
					}
					data = yamlData
				}
			}

			// Build a state from the input data.
			state := engine.NewState()
			for k, v := range data {
				state.Set(k, v)
			}

			// Evaluate each assertion expression. One Masker for the whole
			// invocation, as the run reporters do, so a number that appears
			// in two lines gets the same token. AssertionError embeds the
			// ACTUAL value it compared, so an unmasked FAIL line put a live
			// MSISDN from the input file straight into a CI log — the run
			// path masked this same error in every reporter; this path did
			// not.
			masker := reporter.NewMasker()
			hasFailure := false
			for _, expr := range args {
				if err := engine.EvaluateAssertionPublic(expr, state); err != nil {
					fmt.Fprintf(os.Stderr, "FAIL: %s\n", masker.Mask(err.Error()))
					hasFailure = true
				} else {
					fmt.Fprintf(os.Stdout, "PASS: %s\n", masker.Mask(expr))
				}
			}

			if hasFailure {
				return &exitError{code: exitTestFailure, msg: "one or more assertions failed"}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&input, "input", "", "Path to JSON or YAML input file")

	return cmd
}

// yamlUnmarshal wraps gopkg.in/yaml.v3 for the assert command.
func yamlUnmarshal(data []byte, v interface{}) error {
	return scenario.ParseRawYAML(data, v)
}

const bundledLibraryNotice = "No scenarios/ directory here — listing the copies bundled in this binary.\n" +
	"Run `cellrift init` to write them to ./scenarios, or `cellrift selftest` to see one pass."

func printLibraryTable(w io.Writer, entries []scenario.LibraryEntry) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tPROTOCOLS\tDESCRIPTION\tPATH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			e.Name,
			strings.Join(e.Protocols, ", "),
			e.Description,
			e.Path,
		)
	}
	return tw.Flush()
}

// scenarioDirs lists the directories to scan for built-in scenarios.
// The Docker image copies scenarios to /scenarios; local builds use ./scenarios.
var scenarioDirs = []string{"scenarios", "/scenarios"}

func listCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available built-in scenario templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			entries := scenario.ScanLibrary(scenarioDirs...)
			if len(entries) == 0 {
				// Outside a clone there is no scenarios/ directory; fall back
				// to the copy embedded in the binary so `cellrift list` is
				// never an empty first contact for a `go install` user.
				entries = scenario.ScanLibraryFS(scenarios.FS)
				fmt.Fprintln(os.Stderr, bundledLibraryNotice)
			}

			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			return printLibraryTable(os.Stdout, entries)
		},
	}

	cmd.Flags().StringVar(&format, "format", "table", "Output format: table, json")

	return cmd
}
