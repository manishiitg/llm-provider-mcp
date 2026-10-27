package llmerrors

import (
	"errors"
	"strings"
	"testing"
)

// PLAT-872-64: useful provider reasons must not expose credentials in banners or retry rows.
func TestProviderDisplayKeepsReasonAndMasksCredentialsBeforeClipping(t *testing.T) {
	for _, tc := range []struct{ raw, code, step string }{
		{"Cursor SDK turn failed: Model unavailable on Plus. Switch to an included model to continue.", "billing_error", "Choose another model"},
		{"invalid API key; API_KEY=private-value", "authentication_failed", "Sign in again"},
		{"Cursor SDK turn failed: [unknown] Invalid User API Key", "authentication_failed", "Sign in again"},
		{"Cursor SDK did not start: Sign in again to use Cursor.", "authentication_failed", "Sign in again"},
		{"model not found", "provider_unavailable", "Choose another model"},
		{"internal error: upstream refused; Bearer private-value\nsecond line", "internal_error", "Try again"},
		{`internal error: {"api_key":"` + strings.Repeat("private-value", 30) + `"}`, "internal_error", "Try again"},
	} {
		d := ProviderDisplay(errors.New(tc.raw))
		if d.Code != tc.code || !strings.Contains(d.NextStep, tc.step) || d.Message == "" || strings.Contains(d.Message, "private-value") || strings.Contains(d.Message, "second line") || len([]rune(d.Message)) > 200 {
			t.Fatalf("unexpected display: %+v", d)
		}
		if tc.code != "internal_error" && d.Retryable {
			t.Fatalf("terminal refusal was retryable: %+v", d)
		}
	}
}
