// Package config loads and validates the bot configuration from the process
// environment. It is read once at startup and never mutated afterwards.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Environment variables read by [Load].
const (
	EnvDiscordToken = "DISCORD_TOKEN"
	EnvGuildID      = "DISCORD_GUILD_ID"
	EnvLogLevel     = "LOG_LEVEL"
)

// Config holds every value the bot needs to start.
type Config struct {
	// DiscordToken authenticates the bot against the Discord gateway. Required.
	DiscordToken string

	// GuildID scopes slash-command registration to a single guild, where new
	// commands appear immediately. Empty registers them globally instead, which
	// Discord may take up to an hour to propagate.
	GuildID string

	// LogLevel is the minimum level the default logger emits.
	LogLevel slog.Level
}

// Load reads the configuration from the environment. It returns an error when a
// required variable is missing or a value cannot be parsed.
func Load() (Config, error) {
	cfg := Config{
		DiscordToken: os.Getenv(EnvDiscordToken),
		GuildID:      os.Getenv(EnvGuildID),
		LogLevel:     slog.LevelInfo,
	}

	if cfg.DiscordToken == "" {
		return Config{}, fmt.Errorf("%s is empty", EnvDiscordToken)
	}

	if raw := os.Getenv(EnvLogLevel); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
			return Config{}, fmt.Errorf("parse %s %q: %w", EnvLogLevel, raw, err)
		}
	}

	return cfg, nil
}
