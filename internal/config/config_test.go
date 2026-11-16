package config_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"

	"github.com/kenesparta/cnayp-discord-bot/internal/config"
)

// every variable Load reads, so each case starts from a clean environment.
var every = []string{
	config.EnvMode, config.EnvLogLevel, config.EnvPublicKey, config.EnvDiscordToken,
	config.EnvGuildID, config.EnvEventsChannelID, config.EnvStateTable,
	config.EnvAdapterPort, config.EnvPort, config.EnvLambdaFunction,
	config.EnvAccessKeyID, config.EnvSecretAccessKey, config.EnvSessionToken, config.EnvRegion,
}

func loadWith(t *testing.T, env map[string]string) (config.Config, error) {
	t.Helper()
	for _, key := range every {
		t.Setenv(key, env[key])
	}
	return config.Load()
}

func publicKeyHex(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(public)
}

func TestLoadInteractionsNeedsOnlyThePublicKey(t *testing.T) {
	key := publicKeyHex(t)
	cfg, err := loadWith(t, map[string]string{
		config.EnvMode:        "interactions",
		config.EnvPublicKey:   key,
		config.EnvAdapterPort: "8080",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeInteractions || hex.EncodeToString(cfg.PublicKey) != key || cfg.Addr != ":8080" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.DiscordToken != "" {
		t.Error("the interactions function picked up a token it must not hold")
	}
}

func TestLoadAnnounceOnLambda(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		config.EnvMode:            "announce",
		config.EnvDiscordToken:    "t0ken",
		config.EnvGuildID:         "123",
		config.EnvEventsChannelID: "1539531942754517072",
		config.EnvStateTable:      "cnayp-discord-bot-state",
		config.EnvLambdaFunction:  "cnayp_discord_bot_announcer",
		config.EnvRegion:          "us-east-1",
		config.EnvAccessKeyID:     "AKID",
		config.EnvSecretAccessKey: "secret",
		config.EnvSessionToken:    "session",
		config.EnvLogLevel:        "debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateTable != "cnayp-discord-bot-state" || cfg.Region != "us-east-1" ||
		cfg.Credentials.SessionToken != "session" || cfg.LogLevel != slog.LevelDebug || cfg.Addr != ":8080" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadRefuses(t *testing.T) {
	key := publicKeyHex(t)
	announce := func(extra map[string]string) map[string]string {
		env := map[string]string{config.EnvMode: "announce", config.EnvDiscordToken: "t0ken"}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	tests := []struct {
		name string
		env  map[string]string
		// mention is a phrase the error must carry, so an operator can act on it.
		mention string
	}{
		{name: "no mode", env: map[string]string{}, mention: config.EnvMode},
		{name: "unknown mode", env: map[string]string{config.EnvMode: "gateway"}, mention: config.EnvMode},
		{name: "interactions without a key", env: map[string]string{config.EnvMode: "interactions"}, mention: config.EnvPublicKey},
		{name: "a truncated key", env: map[string]string{config.EnvMode: "interactions", config.EnvPublicKey: key[:60]}, mention: config.EnvPublicKey},
		{name: "announce without a token", env: map[string]string{config.EnvMode: "announce"}, mention: config.EnvDiscordToken},
		{
			name:    "announce on Lambda without a token names passkana",
			env:     map[string]string{config.EnvMode: "announce", config.EnvLambdaFunction: "f"},
			mention: "passkana",
		},
		{
			// A malformed channel id would otherwise produce a bot that starts
			// cleanly and fails every announcement against a channel that cannot
			// exist.
			name:    "an unusable events channel",
			env:     announce(map[string]string{config.EnvEventsChannelID: "#eventos"}),
			mention: config.EnvEventsChannelID,
		},
		{
			// In memory every cold start would be a first run, seeding silently
			// and announcing nothing, every day.
			name:    "announcing on Lambda without a table",
			env:     announce(map[string]string{config.EnvEventsChannelID: "1", config.EnvLambdaFunction: "f"}),
			mention: config.EnvStateTable,
		},
		{
			name:    "a table without credentials",
			env:     announce(map[string]string{config.EnvStateTable: "t", config.EnvRegion: "us-east-1"}),
			mention: config.EnvAccessKeyID,
		},
		{
			name:    "an unparsable log level",
			env:     announce(map[string]string{config.EnvLogLevel: "chatty"}),
			mention: config.EnvLogLevel,
		},
		{name: "a port that is not a number", env: announce(map[string]string{config.EnvPort: "http"}), mention: "port"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadWith(t, tt.env)
			if err == nil {
				t.Fatalf("Load() = %+v, want an error", cfg)
			}
			if !strings.Contains(err.Error(), tt.mention) {
				t.Errorf("error %q does not mention %q", err, tt.mention)
			}
		})
	}
}

func TestLoadAnnounceLocallyKeepsStateInMemory(t *testing.T) {
	// Off Lambda an empty table is the in-memory store, for running locally.
	cfg, err := loadWith(t, map[string]string{
		config.EnvMode:            "announce",
		config.EnvDiscordToken:    "t0ken",
		config.EnvEventsChannelID: "1539531942754517072",
	})
	if err != nil || cfg.StateTable != "" {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}
