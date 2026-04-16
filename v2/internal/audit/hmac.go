package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// computeEventHMAC returns the HMAC-SHA256 hex string for an audit event,
// chained from the previous event's HMAC. This implements the tamper-evidence
// contract: each event's HMAC covers (prev_hmac || canonical_json(event)),
// forming an append-only chain the server can verify incrementally.
//
// Computation: HMAC-SHA256(psk, prev_hmac_hex || canonical_json_bytes)
//
// canonical_json follows RFC 8785 JCS (lexicographic key sort at every level).
// Go's json.Marshal on map[string]any already sorts keys, so we marshal the
// event to a map (dropping the hmac field) and re-marshal for canonical form.
//
// prevHMAC is the hex-encoded HMAC of the previous event in the chain, or ""
// for the first event after a chain reset / PSK rotation.
func computeEventHMAC(psk, prevHMAC string, ev Event) (string, error) {
	canonical, err := canonicalJSON(ev)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(psk))
	mac.Write([]byte(prevHMAC))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// canonicalJSON produces RFC 8785 JCS-compatible JSON for the event, omitting
// the HMAC field itself (it's the output, not the input). Go's json.Marshal
// on map[string]any sorts keys lexicographically at every nesting level,
// which satisfies JCS for our event shape (no arrays with mixed types).
func canonicalJSON(ev Event) ([]byte, error) {
	// Build a map representation without the hmac field.
	m := map[string]any{
		"seq":      ev.Seq,
		"ts":       ev.TS,
		"category": ev.Category,
		"severity": ev.Severity,
		"actor":    ev.Actor,
		"message":  ev.Message,
	}
	if len(ev.Details) > 0 {
		// Ensure details keys are sorted (Go map iteration is random,
		// but json.Marshal sorts map keys).
		sorted := make(map[string]string, len(ev.Details))
		keys := make([]string, 0, len(ev.Details))
		for k := range ev.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sorted[k] = ev.Details[k]
		}
		m["details"] = sorted
	}
	return json.Marshal(m)
}
