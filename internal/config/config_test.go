package config_test

import (
	"log/slog"
	"testing"

	"github.com/kenesparta/cnayp-discord-bot/internal/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    config.Config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{config.EnvDiscordToken: "t0ken"},
			want: config.Config{DiscordToken: "t0ken", LogLevel: slog.LevelInfo},
		},
		{
			name: "all values",
			env: map[string]string{
				config.EnvDiscordToken: "t0ken",
				config.EnvGuildID:      "123",
				config.EnvLogLevel:     "debug",
			},
			want: config.Config{DiscordToken: "t0ken", GuildID: "123", LogLevel: slog.LevelDebug},
		},
		{
			name:    "missing token",
			env:     map[string]string{config.EnvGuildID: "123"},
			wantErr: true,
		},
		{
			name: "unparsable log level",
			env: map[string]string{
				config.EnvDiscordToken: "t0ken",
				config.EnvLogLevel:     "chatty",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{config.EnvDiscordToken, config.EnvGuildID, config.EnvLogLevel} {
				t.Setenv(key, tt.env[key])
			}

			have, err := config.Load()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() = %+v, want error", have)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if have != tt.want {
				t.Errorf("Load() = %+v, want %+v", have, tt.want)
			}
		})
	}
}
