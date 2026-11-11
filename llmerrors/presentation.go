package llmerrors

import (
	"errors"
	"regexp"
	"strings"
)

// Display is the server's concise, credential-masked explanation of a provider failure.
// Keep the provider's reason separate from the next step so clients need no error-policy copy.
type Display struct {
	Code      string
	Message   string
	NextStep  string
	Retryable bool
}

var displaySecrets = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)("(?:api[_-]?key|token|authorization|password|secret)"\s*:\s*")[^"]*(")`), "${1}[redacted]${2}"},
	{regexp.MustCompile(`(?i)\b(Bearer\s+)[^\s"']+`), "${1}[redacted]"},
	{regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:TOKEN|KEY|SECRET|PASSWORD))\s*[=:]\s*[^\s"']+`), "${1}=[redacted]"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`), "[redacted]"},
}
var displayWrapper = regexp.MustCompile(`(?i)^(?:selected LLM failed:\s*|llm error:\s*|[^\s]+/[^\s]+ \[[^\]]+\]:\s*|Cursor SDK (?:turn failed|did not start):\s*)`)

// ProviderDisplay is used for both retry notices and failed-turn metadata.
// Mask before truncating: clipping a secret first could leave an unrecognized fragment.
func ProviderDisplay(err error) Display {
	if err == nil {
		return Display{}
	}
	classified := Classify("", "", err)
	var typed *Error
	message := err.Error()
	if errors.As(classified, &typed) && typed.Err != nil {
		message = typed.Err.Error()
	}
	message = diagnosticPrefix(message)
	for _, rule := range displaySecrets {
		message = rule.pattern.ReplaceAllString(message, rule.replacement)
	}
	message = strings.TrimSpace(strings.SplitN(message, "\n", 2)[0])
	for {
		next := displayWrapper.ReplaceAllString(message, "")
		if next == message {
			break
		}
		message = next
	}
	runes := []rune(message)
	if len(runes) > 200 {
		message = string(runes[:199]) + "…"
	}
	d := Display{Code: "internal_error", Message: message, NextStep: "Open technical details and share them with your admin.", Retryable: IsRetryable(classified)}
	switch KindOf(classified) {
	case KindBilling:
		d.Code = "billing_error"
		d.NextStep = "Check this account's plan or billing."
		lower := strings.ToLower(message)
		if strings.Contains(lower, "model") {
			d.NextStep = "Choose another model or model setting."
		}
	case KindAuth:
		d.Message = "This account's sign-in was rejected."
		d.Code = "authentication_failed"
		d.NextStep = "Sign in again in Providers."
		if strings.Contains(strings.ToLower(message), "api key") || strings.Contains(message, "API_KEY_INVALID") {
			d.Message = "This account's API key was rejected."
			d.NextStep = "Update this account's key in Providers."
		}
	case KindModelNotFound:
		d.Message = "The selected model is unavailable."
		d.Code = "provider_unavailable"
		d.NextStep = "Choose another model."
	case KindQuotaExhausted:
		d.Message = "This account has reached its usage limit."
		d.Code = "quota_exhausted"
		d.NextStep = "Check this account’s usage in Providers."
	case KindRateLimit:
		d.Message = "The provider is receiving too many requests."
		d.Code = "rate_limited"
		d.NextStep = "Wait briefly and try again."
	case KindCanceled:
		d.Message = "The response was cancelled."
		d.Code = "cancelled"
		d.NextStep = ""
	case KindContextTooLong:
		d.Message = "This conversation is too long for the selected model."
		d.Code = "configuration_error"
		d.NextStep = "Start a new chat with a shorter conversation."
	case KindServerError:
		d.Code = "provider_unavailable"
		d.Message = "The provider could not finish this response."
		d.NextStep = "Wait briefly and try again."
	case KindNetwork:
		d.Code = "provider_unavailable"
		d.Message = "The connection to the provider was interrupted."
		d.NextStep = "Try this message again."
	case KindTimeout:
		d.Code = "provider_unavailable"
		d.Message = "The provider took too long to respond."
		d.NextStep = "Try this message again."
	case KindInvalidRequest:
		d.Code = "configuration_error"
		d.Message = "The provider could not accept this request."
		d.NextStep = "Share the technical details with your admin."
	case KindUserInputRequired:
		d.Code = "input_required"
		d.Message = "The agent needs your answer before it can continue."
		d.NextStep = "Answer the pending question in this chat."
	}
	// Raw JSON, HTML and diagnostic wrappers belong in technical details.
	if strings.HasPrefix(d.Message, "{") || strings.HasPrefix(d.Message, "<") || d.Code == "internal_error" {
		d.Message = "The response could not be completed."
	}
	return d
}
