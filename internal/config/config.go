// Package config loads and validates the bot configuration from the process
// environment. It is read once at startup and never mutated afterwards, and it
// is the only package that reads the environment.
//
// The one image runs as two Lambda functions (personal-infra §5.24), and
// BOT_MODE says which: "interactions" answers the slash commands Discord
// delivers over HTTPS, "announce" performs the scheduled runs. Each mode
// requires only what it uses, so the interactions function holds no secret.
package config

import (
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/kenesparta/cnayp-discord-bot/internal/awsapi"
	"github.com/kenesparta/cnayp-discord-bot/internal/discord"
)

// Environment variables read by [Load].
const (
	EnvMode            = "BOT_MODE"
	EnvLogLevel        = "LOG_LEVEL"
	EnvPublicKey       = "DISCORD_PUBLIC_KEY"
	EnvDiscordToken    = "DISCORD_TOKEN"
	EnvGuildID         = "DISCORD_GUILD_ID"
	EnvEventsChannelID = "DISCORD_EVENTS_CHANNEL_ID"
	EnvStateTable      = "STATE_TABLE"
)

// Environment variables the platform sets rather than personal-infra.
const (
	// EnvAdapterPort is the port the Lambda Web Adapter forwards requests to.
	EnvAdapterPort = "AWS_LWA_PORT"
	// EnvPort is the conventional fallback, for running the image elsewhere.
	EnvPort = "PORT"
	// EnvLambdaFunction is set in every Lambda execution environment.
	EnvLambdaFunction = "AWS_LAMBDA_FUNCTION_NAME"
	// The execution role's credentials and the region, also set by Lambda.
	EnvAccessKeyID     = "AWS_ACCESS_KEY_ID"
	EnvSecretAccessKey = "AWS_SECRET_ACCESS_KEY"
	EnvSessionToken    = "AWS_SESSION_TOKEN"
	EnvRegion          = "AWS_REGION"
)

// A Mode is which of the two functions this process is.
type Mode string

// The modes.
const (
	// ModeInteractions answers the interactions Discord sends to the Function
	// URL. It needs the application's public key and nothing secret.
	ModeInteractions Mode = "interactions"
	// ModeAnnounce performs the runs the schedule invokes, and registers the
	// slash commands when asked. It holds the bot token and the state table.
	ModeAnnounce Mode = "announce"
)

// Config holds every value the bot needs to start.
type Config struct {
	Mode Mode
	// Addr is where the HTTP server listens: ":" and the Web Adapter's port.
	Addr string
	// LogLevel is the minimum level the default logger emits.
	LogLevel slog.Level

	// PublicKey verifies that an interaction came from Discord. Interactions
	// mode only.
	PublicKey ed25519.PublicKey

	// DiscordToken authenticates the bot's REST calls. Announce mode only.
	DiscordToken string
	// GuildID scopes slash-command registration to a single guild, where new
	// commands appear immediately. Empty registers them globally instead, which
	// Discord may take up to an hour to propagate.
	GuildID string
	// EventsChannelID is the channel the bot announces upcoming cloud native
	// events in. Empty leaves the announcer switched off; command registration
	// still works.
	EventsChannelID string
	// StateTable is the DynamoDB table holding the announcer's records. Empty
	// keeps them in memory, which only makes sense off Lambda.
	StateTable string
	// Region and Credentials sign the calls to StateTable.
	Region      string
	Credentials awsapi.Credentials
}

// Load reads the configuration from the environment. It returns an error when a
// required variable is missing or a value cannot be parsed.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	cfg := Config{Mode: Mode(getenv(EnvMode)), LogLevel: slog.LevelInfo}
	if raw := getenv(EnvLogLevel); raw != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(strings.ToUpper(raw))); err != nil {
			return Config{}, fmt.Errorf("parse %s %q: %w", EnvLogLevel, raw, err)
		}
	}

	port := firstOf(getenv(EnvAdapterPort), getenv(EnvPort), "8080")
	if !isDigits(port) {
		return Config{}, fmt.Errorf("port %q is not a number", port)
	}
	cfg.Addr = ":" + port

	onLambda := getenv(EnvLambdaFunction) != ""
	switch cfg.Mode {
	case ModeInteractions:
		key, err := discord.ParsePublicKey(getenv(EnvPublicKey))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvPublicKey, err)
		}
		cfg.PublicKey = key
	case ModeAnnounce:
		if err := loadAnnounce(&cfg, getenv, onLambda); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("%s is %q, want %q or %q", EnvMode, cfg.Mode, ModeInteractions, ModeAnnounce)
	}
	return cfg, nil
}

func loadAnnounce(cfg *Config, getenv func(string) string, onLambda bool) error {
	cfg.DiscordToken = getenv(EnvDiscordToken)
	if cfg.DiscordToken == "" {
		if onLambda {
			// On Lambda the token is to come from passkana (personal-infra
			// §5.23), whose client is not integrated yet.
			return fmt.Errorf("%s is empty; on Lambda it is to come from passkana, which is not integrated yet", EnvDiscordToken)
		}
		return fmt.Errorf("%s is empty", EnvDiscordToken)
	}

	cfg.GuildID = getenv(EnvGuildID)
	cfg.EventsChannelID = getenv(EnvEventsChannelID)
	// A channel id is a snowflake, and a malformed one is indistinguishable from
	// a working bot that never posts: every announcement would fail against a
	// channel that cannot exist. Catch it at startup instead.
	for name, id := range map[string]string{EnvGuildID: cfg.GuildID, EnvEventsChannelID: cfg.EventsChannelID} {
		if id != "" && !isDigits(id) {
			return fmt.Errorf("%s %q is not a Discord id", name, id)
		}
	}

	cfg.StateTable = getenv(EnvStateTable)
	if cfg.StateTable == "" {
		// In memory, every cold start would be a first run: it would seed
		// silently and announce nothing, every day, and look healthy doing it.
		if onLambda && cfg.EventsChannelID != "" {
			return fmt.Errorf("%s is empty on Lambda: every run would be a first run and announce nothing", EnvStateTable)
		}
		return nil
	}
	cfg.Region = getenv(EnvRegion)
	cfg.Credentials = awsapi.Credentials{
		AccessKeyID:     getenv(EnvAccessKeyID),
		SecretAccessKey: getenv(EnvSecretAccessKey),
		SessionToken:    getenv(EnvSessionToken),
	}
	if cfg.Region == "" || cfg.Credentials.AccessKeyID == "" || cfg.Credentials.SecretAccessKey == "" {
		return fmt.Errorf("%s is set, so %s, %s and %s must be too", EnvStateTable, EnvRegion, EnvAccessKeyID, EnvSecretAccessKey)
	}
	return nil
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// isDigits reports whether s is a non-empty run of digits: the shape of a
// Discord id, and of a port.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
