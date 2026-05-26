package channel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// AgentRunner abstracts the agent execution for channel use.
type AgentRunner interface {
	CreateSession(ctx context.Context, agentID string) (sessionID string, err error)
	Run(ctx context.Context, sessionID, message string) (response string, err error)
}

// REPLConfig configures the CLI REPL channel.
type REPLConfig struct {
	Prompt   string
	AgentID  string
	Logger   *slog.Logger
	ExitCmds []string
}

func (c REPLConfig) withDefaults() REPLConfig {
	if c.Prompt == "" {
		c.Prompt = "helixon> "
	}
	if c.AgentID == "" {
		c.AgentID = "helixon-cli"
	}
	if len(c.ExitCmds) == 0 {
		c.ExitCmds = []string{"/exit", "/quit", "/bye"}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// REPL implements a CLI read-eval-print loop over stdin/stdout.
type REPL struct {
	agent  AgentRunner
	cfg    REPLConfig
	logger *slog.Logger
}

// NewREPL creates a REPL channel wired to the given agent runner.
func NewREPL(agent AgentRunner, cfg REPLConfig) *REPL {
	cfg = cfg.withDefaults()
	return &REPL{
		agent:  agent,
		cfg:    cfg,
		logger: cfg.Logger.With(slog.String("component", "helixon.channel.repl")),
	}
}

// Run starts the REPL loop, reading from in and writing to out.
// Returns when the user sends an exit command, the input stream closes,
// or the context is cancelled.
func (r *REPL) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	sessionID, err := r.agent.CreateSession(ctx, r.cfg.AgentID)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	r.logger.Info("REPL session started", slog.String("session_id", sessionID))
	fmt.Fprintf(out, "Session: %s\nType /exit to quit.\n\n", sessionID)

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, r.cfg.Prompt)

		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if r.isExit(input) {
			fmt.Fprintln(out, "Goodbye!")
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		start := time.Now()
		response, err := r.agent.Run(ctx, sessionID, input)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Fprintf(out, "[error] %s\n\n", err)
			r.logger.Warn("agent error", slog.String("error", err.Error()))
			continue
		}

		fmt.Fprintf(out, "\n%s\n\n[%s]\n\n", response, elapsed.Round(time.Millisecond))
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}
	return nil
}

func (r *REPL) isExit(input string) bool {
	lower := strings.ToLower(input)
	for _, cmd := range r.cfg.ExitCmds {
		if lower == cmd {
			return true
		}
	}
	return false
}
