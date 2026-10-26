package llmtypes

import "errors"

// ErrCodingAgentInputNotSent means the adapter rejected input before writing
// to the CLI because no native turn could receive it. It is safe to queue the
// input at an active outer turn boundary or start a new turn when idle.
var ErrCodingAgentInputNotSent = errors.New("coding agent input was not sent")
