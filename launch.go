package moirai

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

type LaunchCommand struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Dir     string   `json:"dir,omitempty"`
}

func CommandFor(format Format, saved SessionRef) (LaunchCommand, error) {
	switch format {
	case FormatClaudeCode:
		return LaunchCommand{Program: "claude", Args: []string{"--resume", saved.ID}, Dir: saved.CWD}, nil
	case FormatCodex:
		return LaunchCommand{Program: "codex", Args: []string{"resume", saved.ID}, Dir: saved.CWD}, nil
	case FormatPi:
		return LaunchCommand{Program: "pi", Args: []string{"--session", saved.Location}, Dir: saved.CWD}, nil
	case FormatCampfire:
		return LaunchCommand{Program: "campfire", Args: []string{"--session", saved.Location}, Dir: saved.CWD}, nil
	case FormatCursorDesktop:
		return LaunchCommand{Program: "cursor", Args: []string{saved.CWD}, Dir: saved.CWD}, nil
	case FormatCursor:
		return LaunchCommand{Program: "cursor-agent", Args: []string{"--resume", saved.ID}, Dir: saved.CWD}, nil
	case FormatAntigravity:
		return LaunchCommand{Program: "agy", Args: []string{"--conversation=" + saved.ID}, Dir: saved.CWD}, nil
	case FormatGrok:
		return LaunchCommand{Program: "grok", Args: []string{"--resume", saved.ID}, Dir: saved.CWD}, nil
	case FormatFX:
		return LaunchCommand{Program: "fx", Args: []string{"--resume", saved.ID}, Dir: saved.CWD}, nil
	case FormatOpenCode:
		return LaunchCommand{Program: "opencode", Args: []string{"--session", saved.ID}, Dir: saved.CWD}, nil
	case FormatCowork:
		switch runtime.GOOS {
		case "darwin":
			return LaunchCommand{Program: "open", Args: []string{"-a", "Claude"}, Dir: saved.CWD}, nil
		case "windows":
			return LaunchCommand{Program: "cmd", Args: []string{"/c", "start", "", "claude"}, Dir: saved.CWD}, nil
		default:
			return LaunchCommand{Program: "claude-desktop", Dir: saved.CWD}, nil
		}
	default:
		return LaunchCommand{}, &FormatError{Format: format, Op: "continue", Err: ErrUnsupported}
	}
}

func Launch(ctx context.Context, command LaunchCommand) error {
	if command.Program == "" {
		return fmt.Errorf("%w: empty launch command", ErrUnsupported)
	}
	cmd := exec.CommandContext(ctx, command.Program, command.Args...)
	directory := command.Dir
	if info, err := os.Stat(directory); directory == "" || err != nil || !info.IsDir() {
		directory, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	cmd.Dir = directory
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
