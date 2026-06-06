package llmtypes

import (
	"fmt"
	"math"
)

// StatusExtrasMetaKey is the StatusLine.Metadata key under which an adapter
// exposes "extras" — short, display-ready statusline segments (e.g. plan
// rate-limit usage) that have no first-class field. The value is a []string.
//
// This is the contract that lets every CLI provider expose its own statusline
// without UIs needing per-provider knowledge: each adapter normalizes its
// native schema into these segments, and consumers render them verbatim,
// generically, with no provider-specific branching. The segments flow through
// the existing Metadata passthrough untouched.
const StatusExtrasMetaKey = "status_extras"

// FormatUsageExtra renders a rate-limit window as "<label> <pct>%" (e.g.
// "5h 24%"), the canonical form for the usage segments adapters expose under
// StatusExtrasMetaKey. usedPercent is 0-100.
func FormatUsageExtra(label string, usedPercent float64) string {
	return fmt.Sprintf("%s %d%%", label, int(math.Round(usedPercent)))
}

// SetStatusExtras stores display-ready extra segments on a StatusLine's
// Metadata under StatusExtrasMetaKey, allocating the map if needed. No-op when
// segments is empty so the key is absent rather than carrying an empty list.
func (s *StatusLine) SetStatusExtras(segments []string) {
	if s == nil || len(segments) == 0 {
		return
	}
	if s.Metadata == nil {
		s.Metadata = map[string]interface{}{}
	}
	s.Metadata[StatusExtrasMetaKey] = segments
}
