package paneview

import "testing"

func TestCollapseBlankRuns(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "no blanks", in: "a\nb\nc", want: "a\nb\nc"},
		{name: "single blank preserved", in: "a\n\nb", want: "a\n\nb"},
		{name: "double blank preserved", in: "a\n\n\nb", want: "a\n\n\nb"},
		{name: "triple blank collapsed to two", in: "a\n\n\n\nb", want: "a\n\n\nb"},
		{name: "huge blank run collapsed to two", in: "a\n\n\n\n\n\n\n\nb", want: "a\n\n\nb"},
		{name: "trailing whitespace trimmed per line", in: "a   \nb\t\n  c", want: "a\nb\n  c"},
		{name: "whitespace-only lines treated as blank",
			in:   "a\n   \n\t\n  \nb",
			want: "a\n\n\nb"},
		{name: "spinner-frame pattern from agy capture (stale pruned, active kept)",
			in:   "⣟ Generating...\n\n\n\n\n\n\n\n\n\n\n⣽ Generating.\n",
			want: "⣽ Generating.\n"},
		{name: "stale spinner pattern (pruned when followed by non-blank lines)",
			in:   "⣟ Generating...\n\n\n● Done\n\n",
			want: "● Done\n\n"},
		{name: "multiple stale spinners and one active spinner",
			in:   "⣟ Generating...\n⣽ Generating.\n● Step 1\n⣻ Generating..\n\n",
			want: "● Step 1\n⣻ Generating..\n\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CollapseBlankRuns(tc.in); got != tc.want {
				t.Fatalf("CollapseBlankRuns(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
