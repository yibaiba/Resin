package topology

import (
	"log"
	"strconv"

	"github.com/Resinat/Resin/internal/diagnosticlog"
	"github.com/Resinat/Resin/internal/node"
)

type tcpHealthLog struct {
	event     string
	source    HealthSource
	hash      node.Hash
	requestID string
	outcome   string
	state     node.TCPHealthState
	reason    string
}

func writeTCPHealthLog(entry tcpHealthLog) {
	log.Print(tcpHealthLogLine(entry))
}

func tcpHealthLogLine(entry tcpHealthLog) string {
	return diagnosticlog.Line("[node-health]",
		diagnosticlog.F("event", entry.event),
		diagnosticlog.F("source", string(entry.source)),
		diagnosticlog.F("request_id", entry.requestID),
		diagnosticlog.F("node_hash", entry.hash.Hex()),
		diagnosticlog.F("outcome", entry.outcome),
		diagnosticlog.F("failure_count", strconv.FormatInt(int64(entry.state.FailureCount), 10)),
		diagnosticlog.F("circuit_open", strconv.FormatBool(entry.state.CircuitOpenSince != 0)),
		diagnosticlog.F("validation", string(entry.state.ValidationPending)),
		diagnosticlog.F("generation", strconv.FormatUint(entry.state.ValidationGeneration, 10)),
		diagnosticlog.F("reason", entry.reason),
	)
}

func ordinaryTCPHealthEvent(transition node.TCPHealthTransition, success bool) string {
	switch {
	case success && transition.Changed():
		return "health_restored"
	case success:
		return ""
	case transition.Validation != nil:
		return "validation_requested"
	case transition.Changed():
		return "failure_recorded"
	default:
		return "failure_ignored"
	}
}

func validationResultEvent(validation node.TCPValidation, success bool) string {
	switch {
	case validation.Kind == node.TCPValidationConfirmation && !success:
		return "circuit_opened"
	case validation.Kind == node.TCPValidationRecovery && success:
		return "circuit_recovered"
	case validation.Kind == node.TCPValidationRecovery:
		return "recovery_failed"
	default:
		return "validation_succeeded"
	}
}
