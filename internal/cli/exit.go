package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

type usageError struct{ error }

func (e *usageError) Unwrap() error { return e.error }

// ExitCode distinguishes command failures from Cobra argument/flag validation.
// Cobra returns errors; the executable is responsible for choosing exit codes.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func executeCommand(root *cobra.Command) error {
	started := false
	// Track entry into RunE, after Cobra has validated arguments and required
	// flags. Restore handlers so repeated in-process executions stay independent.
	var restores []func()
	var track func(*cobra.Command)
	track = func(cmd *cobra.Command) {
		if run := cmd.RunE; run != nil {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				started = true
				return run(cmd, args)
			}
			restores = append(restores, func() { cmd.RunE = run })
		}
		for _, child := range cmd.Commands() {
			track(child)
		}
	}
	track(root)
	defer func() {
		for _, restore := range restores {
			restore()
		}
	}()
	err := root.Execute()
	if err != nil && !started {
		return &usageError{err}
	}
	return err
}
