package sessionpolicy

import (
	"testing"
	"time"

	"github.com/kombifyio/go-common/authsession"
)

func TestBrowserSessionLifetimeIsProductPolicy(t *testing.T) {
	if got, want := BrowserSessionLifetime, 7*24*time.Hour; got != want {
		t.Fatalf("BrowserSessionLifetime = %s, want %s", got, want)
	}
	if BrowserSessionLifetime == authsession.DefaultLifetime {
		t.Fatalf("BrowserSessionLifetime must not rely on authsession.DefaultLifetime (%s)", authsession.DefaultLifetime)
	}
}

func TestBrowserSessionConfigAppliesProductLifetime(t *testing.T) {
	cfg := BrowserSessionConfig("techstack", []byte("0123456789abcdef0123456789abcdef"))
	if got, want := cfg.Lifetime, BrowserSessionLifetime; got != want {
		t.Fatalf("Lifetime = %s, want %s", got, want)
	}
	if got, want := cfg.Audience, "techstack"; got != want {
		t.Fatalf("Audience = %q, want %q", got, want)
	}
	if len(cfg.Secret) == 0 {
		t.Fatal("Secret was not carried into browser session config")
	}
}
