package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/anthropic"
	"github.com/hpscript/fastci/internal/gitdiff"
)

// analyzeSystemPrompt frames the model's role: a terse, actionable failure
// diagnosis, not a general chat response.
const analyzeSystemPrompt = `You are helping a developer understand why their test run just failed.
You will be given the command that was run and its combined stdout/stderr output (possibly
truncated to its tail if very long). Identify the most likely root cause and, if possible, a
concrete fix. Be concise and specific - reference the actual file names, line numbers, or error
messages present in the output rather than generic advice. If the output doesn't contain enough
information to diagnose the failure, say so plainly instead of guessing.`

func newAnalyzeCmd() *cobra.Command {
	var model string
	var maxTokens int

	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Ask Claude to diagnose the most recent `fastci test` failure",
		Long: `analyze reads the failure captured by the most recent "fastci test" run
(saved to .fastci-cache/last-failure.json) and sends it to the Anthropic API
for a diagnosis and suggested fix.

This makes a real, billed network request to api.anthropic.com and requires
an ANTHROPIC_API_KEY environment variable. The captured test output - which
may include file paths, source snippets, or other project-specific content
from your test run - is sent to Anthropic as part of that request.

If the most recent "fastci test" run passed (or none has been run yet),
there's nothing to analyze.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnalyze(cmd, analyzeOpts{model: model, maxTokens: maxTokens})
		},
	}

	cmd.Flags().StringVar(&model, "model", anthropic.DefaultModel, "Anthropic model to use")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 1024, "maximum tokens in the model's response")

	return cmd
}

type analyzeOpts struct {
	model     string
	maxTokens int
}

func runAnalyze(cmd *cobra.Command, opts analyzeOpts) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot, err := gitdiff.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("resolving git repository root: %w", err)
	}

	rec, err := readFailureLog(repoRoot)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("fastci: no recent test failure to analyze (the last `fastci test` run passed, or none has been run yet)")
			return nil
		}
		return err
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("fastci analyze: ANTHROPIC_API_KEY is not set")
	}

	fmt.Printf("fastci: asking %s about the %s failure from %s...\n", opts.model, rec.Analyzer, rec.Timestamp.Format("2006-01-02 15:04:05"))

	client := &anthropic.Client{APIKey: apiKey, Model: opts.model, BaseURL: os.Getenv("ANTHROPIC_BASE_URL")}
	answer, err := client.CreateMessage(cmd.Context(), analyzeSystemPrompt, buildAnalyzePrompt(rec), opts.maxTokens)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println(answer)
	return nil
}

func buildAnalyzePrompt(rec failureLog) string {
	prompt := fmt.Sprintf("Command: %s\nTest runner: %s\nExit error: %s\n", rec.Command, rec.Analyzer, rec.Error)
	if rec.Truncated {
		prompt += "(output truncated to its last portion)\n"
	}
	prompt += "\nOutput:\n" + rec.Output
	return prompt
}
