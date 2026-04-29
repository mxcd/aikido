package tools

import "errors"

var (
	ErrDuplicateTool = errors.New("aikido/tools: duplicate tool registration")
	ErrUnknownTool   = errors.New("aikido/tools: unknown tool")
)
