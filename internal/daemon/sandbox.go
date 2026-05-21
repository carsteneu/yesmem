package daemon

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type SandboxConfig struct {
	Enabled             bool  `yaml:"enabled"`
	FallbackUnsandboxed bool  `yaml:"fallback_unsandboxed"`
	AllowedPorts        []int `yaml:"allowed_ports"`
}

type Sandbox struct {
	cfg        SandboxConfig
	binaryPath string
	binaryOnce sync.Once
}

var (
	sandboxLookPath   = exec.LookPath
	sandboxDownloader = downloadAiJail
)

func NewSandbox(cfg SandboxConfig) *Sandbox {
	return &Sandbox{cfg: cfg}
}

func (s *Sandbox) ensureBinary() {
	s.binaryOnce.Do(func() {
		if !s.cfg.Enabled {
			return
		}
		path, err := sandboxLookPath("ai-jail")
		if err == nil {
			s.binaryPath = path
			return
		}
		downloaded, dlErr := sandboxDownloader()
		if dlErr != nil {
			log.Printf("[sandbox] ai-jail not available: %v (fallback=%v)", dlErr, s.cfg.FallbackUnsandboxed)
			return
		}
		s.binaryPath = downloaded
	})
}

func (s *Sandbox) Available() bool {
	s.ensureBinary()
	return s.binaryPath != ""
}

func (s *Sandbox) Run(command string, timeoutSec int) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if s.cfg.Enabled && s.binaryPath != "" {
		args := s.buildArgs(command)
		cmd = exec.CommandContext(ctx, s.binaryPath, args...)
	} else if s.cfg.Enabled && !s.Available() {
		if !s.cfg.FallbackUnsandboxed {
			return "", -1, fmt.Errorf("ai-jail not found and fallback disabled")
		}
		log.Printf("[sandbox] WARNING: ai-jail not found, running unsandboxed")
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	}

	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return string(output), -1, fmt.Errorf("timed out after %ds", timeoutSec)
	}

	return string(output), exitCode, err
}

func (s *Sandbox) buildArgs(command string) []string {
	args := []string{}
	for _, port := range s.cfg.AllowedPorts {
		args = append(args, "--allow-tcp-port", fmt.Sprintf("%d", port))
	}
	args = append(args, "bash", "-c", command)
	return args
}

func (s *Sandbox) RunWithProfile(command string, timeoutSec int, profile SandboxProfile) (string, int, error) {
	if profile == ProfileNone || !s.Available() {
		if profile != ProfileNone && !s.Available() {
			log.Printf("[sandbox] WARNING: profile %s requested but ai-jail not available, running unsandboxed", profile)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		out, err := cmd.CombinedOutput()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				return string(out), -1, err
			}
		}
		return string(out), exitCode, nil
	}
	return s.Run(command, timeoutSec)
}

func (s *Sandbox) BuildSandboxedCommand(command string, profile SandboxProfile) string {
	if profile == ProfileNone || !s.Available() {
		return command
	}
	args := []string{s.binaryPath}
	args = append(args, s.buildArgs(command)...)
	return strings.Join(args, " ")
}

func (s *Sandbox) WrapExecArgs(binary string, args []string, profile SandboxProfile) (string, []string) {
	if profile == ProfileNone || !s.Available() {
		return binary, args
	}
	wrapped := []string{}
	for _, port := range s.cfg.AllowedPorts {
		wrapped = append(wrapped, "--allow-tcp-port", fmt.Sprintf("%d", port))
	}
	wrapped = append(wrapped, binary)
	wrapped = append(wrapped, args...)
	return s.binaryPath, wrapped
}
