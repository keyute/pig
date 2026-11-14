package pigruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"golang.org/x/term"
)

// unsupportedLogins are providers whose OAuth login PiG cannot complete
// without a stable installation device ID (ai.LoginOptions.GetDeviceID: Sign
// in with ChatGPT on "openai"), which Kopi does not keep yet. PiG exposes no
// way to tell which flows need one, so the id is named here.
var unsupportedLogins = []string{"openai"}

// OAuthProviders returns the sorted ids of the providers whose OAuth login
// Kopi can run.
func (r *Runtime) OAuthProviders() []string {
	var ids []string
	for _, p := range r.svcs.ModelRuntime().GetProviders() {
		if p.Auth.OAuth != nil && !slices.Contains(unsupportedLogins, p.ID) {
			ids = append(ids, p.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// Login runs the provider's OAuth flow, answering its prompts from in and
// printing its instructions to out. PiG persists the credential in the agent
// dir's auth.json; Kopi never sees it written or printed, and no browser is
// opened.
func (r *Runtime) Login(ctx context.Context, provider string, in io.Reader, out io.Writer) error {
	p := r.svcs.ModelRuntime().GetProvider(provider)
	if p == nil {
		return fmt.Errorf("failed to log in to %q: %w", provider, ErrUnknownProvider)
	}
	if p.Auth.OAuth == nil {
		return fmt.Errorf("failed to log in to %q: %w", provider, ErrNoOAuth)
	}
	if slices.Contains(unsupportedLogins, provider) {
		return fmt.Errorf("failed to log in to %q: Kopi does not support this provider's login yet", provider)
	}
	l := &loginIO{in: in, lines: bufio.NewReader(in), out: out}
	if _, err := r.svcs.ModelRuntime().Login(ctx, provider, ai.CredentialOAuth, ai.AuthInteraction{Prompt: l.prompt, Notify: l.notify}); err != nil {
		return fmt.Errorf("failed to log in to %q: %w", provider, err)
	}
	return nil
}

type loginIO struct {
	in    io.Reader
	lines *bufio.Reader
	out   io.Writer
	// pending is the read still in flight after its prompt's ctx ended; the
	// next prompt takes its answer instead of starting a concurrent read.
	// PiG joins a cancelled prompt before issuing the next, so prompts never
	// overlap.
	pending chan readResult
}

type readResult struct {
	text string
	err  error
}

func (l *loginIO) prompt(ctx context.Context, p ai.AuthPrompt) (string, error) {
	switch p := p.(type) {
	case ai.AuthTextPrompt:
		l.ask(p.Message, p.Placeholder)
		return l.read(ctx, l.readLine)
	case ai.AuthManualCodePrompt:
		l.ask(p.Message, p.Placeholder)
		return l.read(ctx, l.readLine)
	case ai.AuthSecretPrompt:
		l.ask(p.Message, p.Placeholder)
		return l.read(ctx, l.readSecret)
	case ai.AuthSelectPrompt:
		return l.selectOption(ctx, p)
	}
	return "", fmt.Errorf("unsupported login prompt %q", p.Type())
}

func (l *loginIO) notify(e ai.AuthEvent) {
	switch e := e.(type) {
	case ai.AuthURLEvent:
		fmt.Fprintf(l.out, "Open this URL in a browser to log in:\n  %s\n", e.URL)
		if e.Instructions != "" {
			fmt.Fprintln(l.out, e.Instructions)
		}
	case ai.AuthDeviceCodeEvent:
		fmt.Fprintf(l.out, "Visit %s and enter the code %s\n", e.VerificationURI, e.UserCode)
	case ai.AuthProgressEvent:
		fmt.Fprintln(l.out, e.Message)
	case ai.AuthInfoEvent:
		fmt.Fprintln(l.out, e.Message)
		for _, link := range e.Links {
			fmt.Fprintf(l.out, "  %s\n", link.URL)
		}
	}
}

func (l *loginIO) ask(message, placeholder string) {
	if placeholder != "" {
		fmt.Fprintf(l.out, "%s [%s] ", message, placeholder)
		return
	}
	fmt.Fprintf(l.out, "%s ", message)
}

func (l *loginIO) selectOption(ctx context.Context, p ai.AuthSelectPrompt) (string, error) {
	fmt.Fprintln(l.out, p.Message)
	for i, o := range p.Options {
		if o.Description != "" {
			fmt.Fprintf(l.out, "  %d) %s - %s\n", i+1, o.Label, o.Description)
			continue
		}
		fmt.Fprintf(l.out, "  %d) %s\n", i+1, o.Label)
	}
	fmt.Fprint(l.out, "Choice: ")
	answer, err := l.read(ctx, l.readLine)
	if err != nil {
		return "", err
	}
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(p.Options) {
		return p.Options[n-1].ID, nil
	}
	if slices.ContainsFunc(p.Options, func(o ai.AuthSelectOption) bool { return o.ID == answer }) {
		return answer, nil
	}
	return "", fmt.Errorf("invalid choice %q", answer)
}

// read runs fn off the prompt's goroutine so a cancelled ctx (the OAuth
// callback server winning against a pasted code) returns at once.
func (l *loginIO) read(ctx context.Context, fn func() (string, error)) (string, error) {
	if l.pending == nil {
		ch := make(chan readResult, 1)
		l.pending = ch
		go func() {
			text, err := fn()
			ch <- readResult{text: text, err: err}
		}()
	}
	select {
	case res := <-l.pending:
		l.pending = nil
		return res.text, res.err
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}
}

func (l *loginIO) readLine() (string, error) {
	line, err := l.lines.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("failed to read login input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func (l *loginIO) readSecret() (string, error) {
	f, ok := l.in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return l.readLine()
	}
	secret, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(l.out)
	if err != nil {
		return "", fmt.Errorf("failed to read login input: %w", err)
	}
	return string(secret), nil
}
