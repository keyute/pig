package pigruntime

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oauthProvider  = "kopi-oauth"
	syntheticToken = "synthetic-token-5d1e"
	loginURL       = "https://login.example/authorize?state=kopi"
)

// oauthTestProvider is a native provider whose only auth method is a fake
// OAuth flow: it shows a URL, asks for a pasted code and a team, and returns a
// synthetic token.
func oauthTestProvider(t *testing.T) *ai.ModelsProvider {
	t.Helper()
	p := *ai.NewFauxProvider(ai.FauxConfig{ProviderID: oauthProvider, Model: "oauth-1"}).Provider()
	p.Auth = ai.ProviderAuth{OAuth: &ai.OAuthAuth{
		Name: "Kopi test OAuth",
		Login: func(ctx context.Context, in ai.AuthInteraction, _ ai.LoginOptions) (ai.Credential, error) {
			in.Notify(ai.AuthURLEvent{URL: loginURL, Instructions: "Paste the code shown after login."})
			code, err := in.Prompt(ctx, ai.AuthManualCodePrompt{Message: "Code:"})
			if err != nil {
				return ai.Credential{}, err
			}
			team, err := in.Prompt(ctx, ai.AuthSelectPrompt{Message: "Team:", Options: []ai.AuthSelectOption{{ID: "red", Label: "Red"}, {ID: "blue", Label: "Blue"}}})
			if err != nil {
				return ai.Credential{}, err
			}
			assert.Equal(t, "code-123", code)
			assert.Equal(t, "blue", team)
			return ai.Credential{Type: ai.CredentialOAuth, Access: syntheticToken, Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli()}, nil
		},
		Refresh: func(_ context.Context, c ai.Credential) (ai.Credential, error) { return c, nil },
		ToAuth:  func(c ai.Credential) (ai.ModelAuth, error) { return ai.ModelAuth{APIKey: c.Access}, nil },
	}}
	return &p
}

func TestLoginPersistsCredentialForFreshRuntime(t *testing.T) {
	sb := newSandbox(t)
	r, _ := newTestRuntime(t, sb)
	require.NoError(t, r.RegisterProvider(oauthTestProvider(t)))
	assert.Contains(t, r.OAuthProviders(), oauthProvider)
	ok, err := r.hasCredential(t.Context(), oauthProvider)
	require.NoError(t, err)
	assert.False(t, ok)

	var out strings.Builder
	require.NoError(t, r.Login(t.Context(), oauthProvider, strings.NewReader("code-123\n2\n"), &out))

	assert.Contains(t, out.String(), loginURL)
	assert.Contains(t, out.String(), "2) Blue")
	assert.NotContains(t, out.String(), syntheticToken)

	authFile := filepath.Join(sb.agentDir, "auth.json")
	info, err := os.Stat(authFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	content, err := os.ReadFile(authFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), syntheticToken)

	fresh, _ := newTestRuntime(t, sb)
	require.NoError(t, fresh.RegisterProvider(oauthTestProvider(t)))
	ok, err = fresh.hasCredential(t.Context(), oauthProvider)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestOAuthProvidersAreSorted(t *testing.T) {
	sb := newSandbox(t)
	r, _ := newTestRuntime(t, sb)
	require.NoError(t, r.RegisterProvider(oauthTestProvider(t)))
	ids := r.OAuthProviders()
	assert.IsNonDecreasing(t, ids)
	assert.Contains(t, ids, oauthProvider)
	assert.NotContains(t, ids, fauxProvider)
}

func TestLoginRejectsProvider(t *testing.T) {
	sb := newSandbox(t)
	r, _ := newTestRuntime(t, sb)
	for _, tc := range []struct {
		name, provider string
		want           error
	}{
		{name: "unknown", provider: "no-such-provider", want: ErrUnknownProvider},
		{name: "without oauth", provider: fauxProvider, want: ErrNoOAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Login(t.Context(), tc.provider, strings.NewReader(""), io.Discard)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestLoginNeedingDeviceIDIsUnsupported(t *testing.T) {
	sb := newSandbox(t)
	r, _ := newTestRuntime(t, sb)

	assert.NotContains(t, r.OAuthProviders(), "openai")
	var out strings.Builder
	err := r.Login(t.Context(), "openai", strings.NewReader(""), &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not support")
	assert.Empty(t, out.String(), "the flow never starts")
}

func TestLoginPromptReturnsWhenContextEnds(t *testing.T) {
	in, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	l := &loginIO{in: in, lines: bufio.NewReader(in), out: io.Discard}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := l.prompt(ctx, ai.AuthManualCodePrompt{Message: "Code:"})
	assert.ErrorIs(t, err, context.Canceled)
}

func TestLoginPromptCancelledHandsPendingReadToNextPrompt(t *testing.T) {
	in, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	l := &loginIO{in: in, lines: bufio.NewReader(in), out: io.Discard}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := l.prompt(ctx, ai.AuthManualCodePrompt{Message: "Code:"})
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, l.pending, "the cancelled prompt leaves its read in flight")

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := l.prompt(t.Context(), ai.AuthManualCodePrompt{Message: "Code:"})
		done <- result{text, err}
	}()
	_, err = io.WriteString(w, "code-123\n")
	require.NoError(t, err)
	res := <-done
	require.NoError(t, res.err)
	assert.Equal(t, "code-123", res.text)
	assert.Nil(t, l.pending)
}
