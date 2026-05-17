package tmuxsize

import (
	"os"
	"strconv"
)

const (
	EnvColumns = "CODING_AGENT_TMUX_COLUMNS"
	EnvRows    = "CODING_AGENT_TMUX_ROWS"

	DefaultColumns = 160
	DefaultRows    = 48

	minColumns = 80
	minRows    = 24
	maxColumns = 300
	maxRows    = 120
)

func Args() []string {
	columns, rows := Size()
	return []string{"-x", strconv.Itoa(columns), "-y", strconv.Itoa(rows)}
}

func Size() (columns, rows int) {
	return parseBoundedEnv(EnvColumns, DefaultColumns, minColumns, maxColumns),
		parseBoundedEnv(EnvRows, DefaultRows, minRows, maxRows)
}

func parseBoundedEnv(name string, fallback, min, max int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}
