package app

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Command struct {
	Name, Usage, Summary string
}

type command struct {
	Command
	run func(c *Controller, ctx context.Context, args string) error
}

// commandTable is a function rather than a package variable because /help
// reads the table back, which would be an initialization cycle.
func commandTable() []command {
	return []command{
		{Command{"plan", "/plan", "switch to plan mode (read-only tools)"}, func(c *Controller, _ context.Context, _ string) error {
			c.EnterPlan()
			return nil
		}},
		{Command{"execute", "/execute", "switch to execute mode"}, func(c *Controller, _ context.Context, _ string) error {
			c.EnterExecute()
			return nil
		}},
		{Command{"status", "/status", "show mode, model, session, running work and tools"}, func(c *Controller, _ context.Context, _ string) error {
			c.notice(renderStatus(c.Status()))
			return nil
		}},
		{Command{"new", "/new", "start a new session"}, func(c *Controller, ctx context.Context, _ string) error {
			return c.NewSession(ctx)
		}},
		{Command{"resume", "/resume [id]", "resume a session, or list sessions without an id"}, (*Controller).resumeCommand},
		{Command{"help", "/help", "list commands"}, func(c *Controller, _ context.Context, _ string) error {
			c.notice(renderHelp(c.Commands()))
			return nil
		}},
		{Command{"quit", "/quit", "exit kopi"}, func(*Controller, context.Context, string) error {
			return ErrQuit
		}},
	}
}

// parse reports whether line is a control command. A "//" prefix is the
// escape for a prompt that starts with "/", so it is not a command.
func parse(line string) (name, args string, isCommand bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") || strings.HasPrefix(line, "//") {
		return "", "", false
	}
	name, args, _ = strings.Cut(line[1:], " ")
	return name, strings.TrimSpace(args), true
}

func (c *Controller) resumeCommand(ctx context.Context, id string) error {
	if id != "" {
		return c.Resume(ctx, id)
	}
	sessions, err := c.Sessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		c.notice("no saved sessions for this workspace")
		return nil
	}
	var b strings.Builder
	b.WriteString("sessions (newest first):")
	for _, s := range sessions {
		first, _, _ := strings.Cut(s.FirstMessage, "\n")
		fmt.Fprintf(&b, "\n  %s  %s  %s", s.ID, s.Modified.Local().Format(time.DateTime), first)
	}
	c.notice(b.String())
	return nil
}

func renderHelp(cmds []Command) string {
	var b strings.Builder
	b.WriteString("commands:")
	for _, cmd := range cmds {
		fmt.Fprintf(&b, "\n  %-13s %s", cmd.Usage, cmd.Summary)
	}
	b.WriteString("\n  //text        send a prompt that starts with /")
	return b.String()
}
