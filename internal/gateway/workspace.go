package gateway

import (
	"encoding/json"
	"weagent/backend/contracts"
)

type workspaceOperation struct {
	Kind    string `json:"kind"`
	Idle    bool   `json:"idle"`
	Write   bool   `json:"write"`
	NewTurn bool   `json:"newTurn"`
	Closed  bool   `json:"closed"`
}

var workspaceRegistry = func() map[string]workspaceOperation {
	var entries []workspaceOperation
	if err := json.Unmarshal(contracts.WorkspaceOperations, &entries); err != nil {
		panic(err)
	}
	result := map[string]workspaceOperation{}
	for _, entry := range entries {
		if _, exists := result[entry.Kind]; exists {
			panic("duplicate workspace operation")
		}
		result[entry.Kind] = entry
	}
	return result
}()

func workspaceTurn(control M) bool {
	return str(control, "action") == "workspace" && workspaceRegistry[str(object(control, "request"), "kind")].NewTurn
}
func (s *Server) workspaceGate(session, b M) error {
	op, ok := workspaceRegistry[str(object(object(b, "control"), "request"), "kind")]
	if !ok {
		return invalid("Unknown workspace operation")
	}
	if str(session, "historyState") == "purged" {
		return apierr(410, "HISTORY_PURGED", "History was purged")
	}
	if op.Kind == "resume" && str(session, "state") != "closed" {
		return apierr(409, "CAPABILITY_UNSUPPORTED", "Only a closed managed native session can resume")
	}
	if str(session, "mode") == "readonly" || str(session, "state") == "closed" && !op.Closed {
		return apierr(409, "READ_ONLY", "A live managed session is required")
	}
	if number(session, "capabilityRevision") != number(b, "capabilityRevision") {
		return apierr(409, "CAPABILITY_CHANGED", "Capabilities changed")
	}
	if !truth(object(session, "capabilities"), "workspace") {
		return apierr(409, "CAPABILITY_UNSUPPORTED", "This Node does not expose workspace operations")
	}
	if op.Idle && isActive(str(session, "state")) {
		return apierr(409, "STALE_TURN", "Wait for the current turn to finish")
	}
	return nil
}

// Transfers retain authentication, exact schemas, session ownership, operation
// receipts and their own per-user rate budget. They are not arbitrary native calls.
func workspaceTransfer(name string, body M) bool {
	if name != "nativeControl" {
		return false
	}
	control := object(body, "control")
	if str(control, "action") != "workspace" {
		return false
	}
	kind := str(object(control, "request"), "kind")
	return kind == "read_file" || kind == "upload_chunk"
}
