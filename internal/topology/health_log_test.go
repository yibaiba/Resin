package topology

import (
	"strings"
	"testing"

	"github.com/Resinat/Resin/internal/node"
)

func TestOrdinaryTCPHealthEvent(t *testing.T) {
	confirmation := node.TCPValidation{Kind: node.TCPValidationConfirmation, Generation: 2}
	tests := []struct {
		name       string
		transition node.TCPHealthTransition
		success    bool
		want       string
	}{
		{name: "failure", transition: changedHealthTransition(0, 1), want: "failure_recorded"},
		{name: "threshold", transition: node.TCPHealthTransition{Validation: &confirmation}, want: "validation_requested"},
		{name: "ignored", want: "failure_ignored"},
		{name: "restored", transition: changedHealthTransition(2, 0), success: true, want: "health_restored"},
		{name: "healthy success", success: true, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ordinaryTCPHealthEvent(test.transition, test.success); got != test.want {
				t.Fatalf("event = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTCPHealthLogLineIncludesSourceRequestAndGeneration(t *testing.T) {
	line := tcpHealthLogLine(tcpHealthLog{
		event: "validation_requested", source: HealthSourcePassive,
		hash: node.Hash{1}, requestID: "request-123",
		outcome: "failure",
		state: node.TCPHealthState{
			FailureCount: 3, ValidationGeneration: 4,
			ValidationPending: node.TCPValidationConfirmation,
		},
	})
	for _, expected := range []string{
		`[node-health]`, `source="passive"`, `request_id="request-123"`,
		`failure_count="3"`, `validation="confirmation"`, `generation="4"`,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("line missing %q: %s", expected, line)
		}
	}
}

func changedHealthTransition(previous, current int32) node.TCPHealthTransition {
	return node.TCPHealthTransition{
		Previous: node.TCPHealthState{FailureCount: previous},
		Current:  node.TCPHealthState{FailureCount: current},
	}
}
