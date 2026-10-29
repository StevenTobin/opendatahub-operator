package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDAGReportStatusExtraction(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": string(metav1.ConditionFalse)},
			},
			"releases": []any{
				map[string]any{"name": "platform", "version": "2.30.0"},
			},
		},
	}}

	assert.Equal(t, string(metav1.ConditionFalse), moduleConditionStatus(obj, "Ready"))
	assert.Equal(t, "2.30.0", moduleReleaseVersion(obj))
	assert.Empty(t, moduleConditionStatus(obj, "Degraded"))
}
