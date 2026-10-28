package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

const (
	domainPlatformOperator    = "platform-operator"
	domainModuleConfiguration = "module-configuration"
	domainTestInfrastructure  = "test-infrastructure"
	domainUnknown             = "unknown"
)

type dagFailure struct {
	Module                 string `json:"module,omitempty"`
	Domain                 string `json:"domain"`
	Stage                  string `json:"stage,omitempty"`
	LastSuccessfulBoundary string `json:"lastSuccessfulBoundary,omitempty"`
	Message                string `json:"message,omitempty"`
}

type dagReport struct {
	Failures []dagFailure `json:"failures"`
}

type result struct {
	CoreStatus      string       `json:"coreStatus"`
	DAGStatus       string       `json:"dagStatus"`
	OperatorVerdict string       `json:"operatorVerdict"`
	Reason          string       `json:"reason"`
	Failures        []dagFailure `json:"failures,omitempty"`
}

func main() {
	coreExit := flag.Int("core-exit", 0, "core E2E process exit code")
	dagExit := flag.Int("dag-exit", -1, "DAG E2E process exit code; -1 means not run")
	dagReportPath := flag.String("dag-report", "", "optional attributed DAG report JSON")
	outputPath := flag.String("output", "", "optional summary JSON output path")
	flag.Parse()

	verdict, exitCode := evaluate(*coreExit, *dagExit, readDAGReport(*dagReportPath))
	printSummary(verdict)
	if *outputPath != "" {
		if err := writeResult(*outputPath, verdict); err != nil {
			fmt.Fprintf(os.Stderr, "write E2E summary: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(exitCode)
}

func readDAGReport(path string) *dagReport {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var report dagReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil
	}
	return &report
}

func evaluate(coreExit, dagExit int, report *dagReport) (result, int) {
	if coreExit != 0 {
		return result{
			CoreStatus:      "failed",
			DAGStatus:       "not-run",
			OperatorVerdict: "failed",
			Reason:          "core or synthetic platform contract failed",
		}, coreExit
	}
	if dagExit == 0 {
		return result{
			CoreStatus:      "passed",
			DAGStatus:       "passed",
			OperatorVerdict: "passed",
			Reason:          "core, synthetic, and production DAG passed",
		}, 0
	}
	if dagExit < 0 {
		return result{
			CoreStatus:      "passed",
			DAGStatus:       "not-run",
			OperatorVerdict: "failed",
			Reason:          "DAG was not run",
		}, 1
	}

	if report == nil || len(report.Failures) == 0 {
		return result{
			CoreStatus:      "passed",
			DAGStatus:       "failed",
			OperatorVerdict: "failed",
			Reason:          "DAG failure has no trustworthy attribution",
		}, dagExit
	}

	allModuleConfiguration := true
	for _, failure := range report.Failures {
		switch failure.Domain {
		case domainModuleConfiguration:
		case domainPlatformOperator, domainTestInfrastructure, domainUnknown:
			allModuleConfiguration = false
		default:
			allModuleConfiguration = false
		}
	}
	if allModuleConfiguration {
		return result{
			CoreStatus:      "passed",
			DAGStatus:       "failed-module-configuration",
			OperatorVerdict: "passed-with-integration-warning",
			Reason:          "operator-owned contracts passed; DAG failures are attributed only to module configuration",
			Failures:        report.Failures,
		}, 0
	}

	return result{
		CoreStatus:      "passed",
		DAGStatus:       "failed",
		OperatorVerdict: "failed",
		Reason:          "DAG contains platform, infrastructure, unknown, or invalidly attributed failures",
		Failures:        report.Failures,
	}, dagExit
}

func printSummary(verdict result) {
	fmt.Println()
	fmt.Println("E2E RESULT SUMMARY")
	fmt.Printf("  CORE + SYNTHETIC: %s\n", verdict.CoreStatus)
	fmt.Printf("  PRODUCTION DAG:   %s\n", verdict.DAGStatus)
	fmt.Printf("  OPERATOR VERDICT: %s\n", verdict.OperatorVerdict)
	fmt.Printf("  REASON:           %s\n", verdict.Reason)
	for _, failure := range verdict.Failures {
		fmt.Printf("  DAG FAILURE: module=%s domain=%s stage=%s boundary=%s message=%s\n",
			failure.Module, failure.Domain, failure.Stage, failure.LastSuccessfulBoundary, failure.Message)
	}
}

func writeResult(path string, verdict result) error {
	data, err := json.MarshalIndent(verdict, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
