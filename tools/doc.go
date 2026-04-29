// Package tools provides a registry, dispatch, and explicit JSON-Schema helpers
// for tools the LLM may call.
//
// Schemas are explicit; the package does not generate schemas from struct types
// (ADR-018). Callers who want struct-driven schemas use invopop/jsonschema:
//
//	import jsonschema "github.com/invopop/jsonschema"
//	schema, _ := json.Marshal(jsonschema.Reflect(&FooArgs{}))
//
// Tool handlers capture the dependencies they need (storage, logger, clock) at
// registration time via closure (ADR-021). The Env passed to a Handler carries
// only fields that genuinely change per dispatch: SessionID and TurnID.
package tools
