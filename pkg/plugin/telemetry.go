package plugin

import (
	"encoding/json"
	"errors"
)

// TelemetryKey is the key under which a plugin reports what it did for an
// operation, inside the JSON object its tool result returns:
//
//	{"pods": [...], "cerberus_telemetry": [{"kind": "step", "message": "scaled web to 3", "target": "prod/web"}]}
//
// The host removes the key before the result reaches any caller, and
// attaches the events — bounded and passed through the plugin's value
// redactor — to the audit record it writes for the operation. A plugin
// enriches that record; it cannot write one. Events name what happened; they
// must never carry a credential or a value the caller did not already see.
const TelemetryKey = "cerberus_telemetry"

// TelemetryEvent is one thing a plugin reports about an operation: a step it
// took, a sub-target it touched, a warning.
type TelemetryEvent struct {
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message,omitempty"`
	Target  string `json:"target,omitempty"`
}

// AttachTelemetry adds events to a tool result's JSON object content under
// TelemetryKey. Content that is not a JSON object is returned unchanged with
// an error: telemetry rides only on object results.
func AttachTelemetry(content json.RawMessage, events ...TelemetryEvent) (json.RawMessage, error) {
	if len(events) == 0 {
		return content, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil || object == nil {
		return content, errors.New("plugin: telemetry attaches only to a JSON object result")
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		return content, err
	}
	object[TelemetryKey] = encoded
	return json.Marshal(object)
}

// SplitTelemetry removes TelemetryKey from a tool result's content and
// returns the content without it and the events it carried. Content that is
// not a JSON object, or has no telemetry, is returned as it was.
func SplitTelemetry(content json.RawMessage) (json.RawMessage, []TelemetryEvent) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil || object == nil {
		return content, nil
	}
	raw, ok := object[TelemetryKey]
	if !ok {
		return content, nil
	}
	delete(object, TelemetryKey)
	var events []TelemetryEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		// Malformed telemetry is dropped, never passed to the caller.
		events = []TelemetryEvent{{Kind: "malformed", Message: "the plugin reported telemetry the host could not read"}}
	}
	stripped, err := json.Marshal(object)
	if err != nil {
		return content, events
	}
	return stripped, events
}
