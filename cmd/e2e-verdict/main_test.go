package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name      string
		coreExit  int
		dagExit   int
		report    *dagReport
		wantCode  int
		wantState string
	}{
		{name: "core failure", coreExit: 2, dagExit: -1, wantCode: 2, wantState: "failed"},
		{name: "all pass", coreExit: 0, dagExit: 0, wantCode: 0, wantState: "passed"},
		{name: "unattributed DAG failure", coreExit: 0, dagExit: 1, wantCode: 1, wantState: "failed"},
		{
			name:     "module configuration only is warning",
			coreExit: 0,
			dagExit:  1,
			report: &dagReport{Failures: []dagFailure{
				{Module: "kserve", Domain: domainModuleConfiguration, Stage: "deployment"},
				{Module: "ray", Domain: domainModuleConfiguration, Stage: "status"},
			}},
			wantCode:  0,
			wantState: "passed-with-integration-warning",
		},
		{
			name:     "platform failure remains blocking",
			coreExit: 0,
			dagExit:  1,
			report: &dagReport{Failures: []dagFailure{
				{Module: "kserve", Domain: domainModuleConfiguration},
				{Domain: domainPlatformOperator, Stage: "dag"},
			}},
			wantCode:  1,
			wantState: "failed",
		},
		{
			name:     "infrastructure failure remains blocking",
			coreExit: 0,
			dagExit:  3,
			report: &dagReport{Failures: []dagFailure{
				{Domain: domainTestInfrastructure},
			}},
			wantCode:  3,
			wantState: "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code := evaluate(tt.coreExit, tt.dagExit, tt.report)
			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantState, got.OperatorVerdict)
		})
	}
}
