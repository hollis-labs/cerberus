package ssh

import contract "github.com/hollis-labs/cerberus/pkg/connector"

// Operation fields are the only keys an SSH operation's config may carry.
// Everything that decides where and how to connect — host, port, user, key,
// host-key settings — comes from the configured resource named by id. The
// admin lane enforces this list (it refuses every other key by name), and the
// operations' input schemas are built from it, so discovery advertises
// exactly what enforcement accepts.
const (
	FieldID         = "id"
	FieldCommand    = "command"
	FieldLocalPath  = "local_path"
	FieldRemotePath = "remote_path"
)

// OperationFields is every key an ssh operation accepts. Connection
// settings are not among them: they come from the configured resource.
var OperationFields = []string{FieldID, FieldCommand, FieldLocalPath, FieldRemotePath}

var fieldSchemas = map[string]map[string]any{
	FieldID:         contract.StringSchema("ID of a configured ssh resource (see `cerberus resource list`). Host, user, key and host-key settings come from the resource."),
	FieldCommand:    contract.StringSchema("Command to execute."),
	FieldLocalPath:  contract.StringSchema("Local path."),
	FieldRemotePath: contract.StringSchema("Path on the remote host."),
}

// operationInputs is an operation's key table over the given fields, each
// described once, all required, with id always first. descriptions overrides
// a field's generic description for this operation.
func operationInputs(descriptions map[string]string, fields ...string) []contract.Input {
	inputs := []contract.Input{contract.RequiredField(FieldID, fieldSchemas[FieldID])}
	for _, field := range fields {
		schema := fieldSchemas[field]
		if text, ok := descriptions[field]; ok {
			schema = contract.StringSchema(text)
		}
		inputs = append(inputs, contract.RequiredField(field, schema))
	}
	return inputs
}

// hostTarget is every ssh operation's target: the configured host resource.
var hostTarget = contract.TargetDescriptor{Kind: "ssh.host", From: []string{FieldID}}
