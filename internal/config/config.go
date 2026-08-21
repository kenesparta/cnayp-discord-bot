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
	EnvDiscordToken    = "DISCORD_TOKEN"
	EnvGuildID         = "DISCORD_GUILD_ID"
	EnvEventsChannelID = "DISCORD_EVENTS_CHANNEL_ID"
	EnvLogLevel        = "LOG_LEVEL"
)

// Config holds every value the bot needs to start.
type Config struct {
	// DiscordToken authenticates the bot against the Discord gateway. Required.
	DiscordToken string

	// GuildID scopes slash-command registration to a single guild, where new
	// commands appear immediately. Empty registers them globally instead, which
	// Discord may take up to an hour to propagate.
	GuildID string

	// EventsChannelID is the channel the bot announces upcoming cloud native
	// events in. Empty leaves the announcer switched off and the bot serving
	// commands only.
	EventsChannelID string

	// LogLevel is the minimum level the default logger emits.
	LogLevel slog.Level
}

// Load reads the configuration from the environment. It returns an error when a
// required variable is missing or a value cannot be parsed.
func Load() (Config, error) {
	cfg := Config{
		DiscordToken:    os.Getenv(EnvDiscordToken),
		GuildID:         os.Getenv(EnvGuildID),
		EventsChannelID: os.Getenv(EnvEventsChannelID),
		LogLevel:        slog.LevelInfo,
	}

	if cfg.DiscordToken == "" {
		return Config{}, fmt.Errorf("%s is empty", EnvDiscordToken)
	}

	// A channel id is a snowflake, and a malformed one is indistinguishable from
	// a working bot that never posts: every announcement would fail against a
	// channel that cannot exist. Catch it at startup instead.
	if cfg.EventsChannelID != "" && !isSnowflake(cfg.EventsChannelID) {
		return Config{}, fmt.Errorf("%s %q is not a channel id", EnvEventsChannelID, cfg.EventsChannelID)
	}

	if raw := os.Getenv(EnvLogLevel); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
			return Config{}, fmt.Errorf("parse %s %q: %w", EnvLogLevel, raw, err)
		}
	}

	return cfg, nil
}

// isSnowflake reports whether id has the shape of a Discord id: a run of digits.
func isSnowflake(id string) bool {
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
