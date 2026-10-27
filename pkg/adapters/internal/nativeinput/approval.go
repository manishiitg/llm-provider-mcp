package nativeinput

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// Only the displayed copy is masked; the provider retains the original input.
var approvalSecretAssignment = regexp.MustCompile(`(?i)\b([A-Z_][A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|ACCESS_KEY)[A-Z0-9_]*|TOKEN|SECRET|PASSWORD|API_KEY)=(?:"[^"\n]*"|'[^'\n]*'|[^\s;]+)`)

// ApproveCommand uses the attended chat's existing scoped answer endpoint.
// Deny is first because the generic card's "use defaults" must never grant
// command execution. Cancellation, missing attendance and expiry never allow.
func ApproveCommand(ctx context.Context, description string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	description = approvalSecretAssignment.ReplaceAllString(description, "$1=[hidden]")
	answers, err := Ask(ctx, []Question{{ID: "command_permission", Question: description,
		Options: []Option{{Label: "Deny"}, {Label: "Allow", Description: "Allow this command once."}}}})
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return len(answers) == 1 && len(answers[0].SelectedLabels) == 1 && strings.EqualFold(answers[0].SelectedLabels[0], "Allow"), nil
}
