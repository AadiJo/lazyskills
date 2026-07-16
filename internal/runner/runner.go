package runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/alvinunreal/lazyskills/internal/compat"
)

const MaxOutputBytes = 32 * 1024

type ExecSpec struct {
	Program string   `json:"program"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
}

type Result struct {
	Program   string   `json:"program"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	ExitCode  int      `json:"exit_code"`
	Err       string   `json:"error,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type Runner interface {
	Run(ExecSpec) Result
}

type OSRunner struct {
	Timeout time.Duration
}

func (r OSRunner) Run(spec ExecSpec) Result {
	result := Result{Program: spec.Program, Args: append([]string{}, spec.Args...), Cwd: spec.Cwd, ExitCode: -1}
	if spec.Program == "" {
		result.Err = "missing program"
		return result
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.Program, spec.Args...)
	cmd.Dir = spec.Cwd
	var stdout, stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result.Stdout, result.Stderr = sanitizeOutput(stdout.String()), sanitizeOutput(stderr.String())
	result.Truncated = stdout.Truncated || stderr.Truncated
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		result.Err = ctx.Err().Error()
		return result
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Err = err.Error()
		}
	}
	return result
}

type cappedBuffer struct {
	buf       bytes.Buffer
	Truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := MaxOutputBytes - b.buf.Len()
	if remaining <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.Truncated = true
		_, _ = b.buf.Write(p[:remaining])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) String() string {
	out := b.buf.String()
	if b.Truncated {
		out += "\n[output truncated]"
	}
	return out
}

func sanitizeOutput(value string) string {
	value = compat.SanitizePreviewContent(value)
	return strings.TrimRight(value, "\n")
}
