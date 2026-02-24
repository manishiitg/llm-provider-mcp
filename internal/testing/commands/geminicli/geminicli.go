package geminicli

import (
	"github.com/spf13/cobra"
)

var GeminiCLICmd = &cobra.Command{
	Use:   "gemini-cli",
	Short: "Gemini CLI provider tests",
}

func init() {
	GeminiCLICmd.AddCommand(GeminiCLIStreamingContentTestCmd)
}
