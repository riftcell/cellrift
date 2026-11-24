package main

import (
	"fmt"
	"os"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/riftcell/cellrift/scenarios"
	"github.com/spf13/cobra"
)

// selftestCmd runs the bundled loopback scenario against in-process mock
// peers. It is the one command a new user can run with no infrastructure and
// see pass, which is how they tell a working install from a broken one —
// every other shipped scenario fails against nothing, by design.
func selftestCmd() *cobra.Command {
	cfg := &runConfig{}
	var timeout int

	cmd := &cobra.Command{
		Use:           "selftest",
		Short:         "Run the bundled loopback scenario against built-in mock peers",
		Long:          selftestLong,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.timeout = time.Duration(timeout) * time.Millisecond
			return cfg.executeSelfTest()
		},
	}

	cmd.Flags().StringVar(&cfg.format, "format", "terminal", "Output format: terminal, junit, json")
	cmd.Flags().StringVar(&cfg.output, "output", "", "Write output to file instead of stdout")
	cmd.Flags().IntVar(&timeout, "timeout", 3000, "Global step timeout in ms")
	cmd.Flags().BoolVar(&cfg.verbose, "verbose", false, "Print all protocol messages")
	cmd.Flags().BoolVar(&cfg.noMask, "no-mask", false, "Do not mask phone numbers in output. Credentials are removed regardless of this flag.")

	return cmd
}

const selftestLong = `Start a mock SIP UAS (UDP) and a strict RFC 6733 mock Diameter HSS (TCP)
on loopback ports, run scenarios/selftest/loopback.yaml against them, and
print the result exactly as "cellrift run" would.

A pass proves this binary, its protocol encoders, the SIP dialog tracker,
the Diameter CER/CEA handshake and the chosen reporter work on this
machine. It proves nothing about any network element you own: the peers
are Cellrift's own mocks. SCTP is not exercised.`

// selfTestPeers holds the two mock peers a self-test runs against.
type selfTestPeers struct {
	uas *sip.MockUASServer
	hss *diameter.MockHSS
}

func startSelfTestPeers() (*selfTestPeers, error) {
	uas, err := sip.StartMockUAS()
	if err != nil {
		return nil, fmt.Errorf("start mock SIP UAS: %w", err)
	}
	hss, err := diameter.StartMockHSS(true)
	if err != nil {
		uas.Close()
		return nil, fmt.Errorf("start mock Diameter HSS: %w", err)
	}
	return &selfTestPeers{uas: uas, hss: hss}, nil
}

func (p *selfTestPeers) Close() {
	p.hss.Close()
	p.uas.Close()
}

// loadSelfTestScenario parses and validates the embedded loopback scenario.
// A failure here is a build defect, not a user error: the file ships inside
// the binary and is validated by the test suite.
func loadSelfTestScenario() (*scenario.Scenario, error) {
	data, err := scenarios.FS.ReadFile(scenarios.SelfTestPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s: %w", scenarios.SelfTestPath, err)
	}
	s, err := scenario.Parse(data, scenarios.SelfTestPath)
	if err != nil {
		return nil, err
	}
	if errs := scenario.Validate(s); len(errs) > 0 {
		return nil, fmt.Errorf("embedded %s: %s", scenarios.SelfTestPath, errs[0].Error())
	}
	return s, nil
}

// executeSelfTest is the body of the selftest command: peers up, scenario
// pointed at them via the same --var path a user would use, then the shared
// run/report/exit-code path.
func (cfg *runConfig) executeSelfTest() error {
	s, err := loadSelfTestScenario()
	if err != nil {
		return &exitError{code: exitExecutionError, msg: err.Error()}
	}
	peers, err := startSelfTestPeers()
	if err != nil {
		return &exitError{code: exitExecutionError, msg: err.Error()}
	}
	defer peers.Close()

	fmt.Fprintf(os.Stderr, "self-test peers: SIP UAS udp://%s, Diameter HSS tcp://%s (in-process mocks)\n",
		peers.uas.Addr(), peers.hss.Addr())

	cfg.vars = append(cfg.vars,
		"pcscf_target="+peers.uas.Addr(),
		"hss_target="+peers.hss.Addr(),
	)
	results, executionError := filterNil([]*engine.RunResult{cfg.executeParsed(s)})
	return cfg.reportAndExit(results, executionError)
}
