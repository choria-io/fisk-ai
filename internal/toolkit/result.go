package toolkit

// CommandResult is the structured result of running a command tool. It is
// returned to the model as JSON; the exit code plus separated output streams let
// the model distinguish success from failure and diagnostics from output.
type CommandResult struct {
	// Command is the command and arguments that were run, without the binary path.
	Command string `json:"command"`
	// ExitCode is the command's exit status; 0 on success.
	ExitCode int `json:"exit_code"`
	// Output is the command's output: stdout and stderr combined in the order they
	// were written, as they would appear in a terminal, so their interleaving (and
	// thus when in the run any diagnostics appeared) is preserved.
	Output string `json:"output"`
	// Truncated is true when Output was capped.
	Truncated bool `json:"truncated,omitempty"`
}
