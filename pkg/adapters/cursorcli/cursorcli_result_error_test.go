package cursorcli

import "testing"

// TestCursorResultErrorMessage is a fast, deterministic tripwire on the
// decision logic only — see cursorResultErrorMessage's doc comment for why
// this bug (unlike its Claude twin) could not be live-verified against real
// cursor-agent: three induced-failure attempts never reached a genuine
// is_error:true stream-json result.
func TestCursorResultErrorMessage(t *testing.T) {
	if msg, isErr := cursorResultErrorMessage(false, "all good", ""); isErr || msg != "" {
		t.Errorf("non-error result must not be flagged: isErr=%v msg=%q", isErr, msg)
	}

	if msg, isErr := cursorResultErrorMessage(true, "model not found", ""); !isErr || msg != "model not found" {
		t.Errorf("error result must surface its content as the message: isErr=%v msg=%q", isErr, msg)
	}

	if msg, isErr := cursorResultErrorMessage(true, "", "stderr text"); !isErr || msg != "stderr text" {
		t.Errorf("error result with empty content must fall back to stderr: isErr=%v msg=%q", isErr, msg)
	}

	if msg, isErr := cursorResultErrorMessage(true, "", ""); !isErr || msg != "" {
		t.Errorf("error result with nothing to say must still flag isErr: isErr=%v msg=%q", isErr, msg)
	}
}
