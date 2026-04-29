package llm

import "encoding/json"

// ToolDef is one tool the model may call. Parameters is a JSON Schema.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
