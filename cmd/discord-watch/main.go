// Command discord-watch opens a Discord gateway session and prints which voice
// channels are populated, and by whom, once per second.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
	"github.com/ernado/lilith/internal/discord"
)

func run(ctx context.Context) error {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		return errors.New("DISCORD_TOKEN is empty")
	}

	client, err := discord.New(token)
	if err != nil {
		return errors.Wrap(err, "create client")
	}

	if err := client.Open(ctx); err != nil {
		return errors.Wrap(err, "open")
	}

	defer func() {
		_ = client.Close()
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var identified bool

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !identified {
				if self, ok := client.Self(); ok {
					fmt.Printf("connected as %s\n", self)
					identified = true
				}
			}

			channels, err := client.PopulatedChannels(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				continue
			}

			printChannels(client.Stats(), channels)
		}
	}
}

// printChannels writes a human-readable snapshot of populated channels, with a
// diagnostic header so an empty result can be told apart from "no guilds".
func printChannels(stats discord.Stats, channels []lilith.DiscordChannel) {
	fmt.Printf("[%s] guilds=%d voice_states=%d populated_channels=%d\n",
		time.Now().Format(time.TimeOnly), stats.Guilds, stats.VoiceStates, len(channels))
	for _, ch := range channels {
		var names []string
		for _, m := range ch.Members {
			name := m.Username
			if m.Nickname != "" {
				name = m.Nickname
			}
			if name == "" {
				name = m.ID
			}

			names = append(names, name)
		}

		fmt.Printf("  %s/%s: %s\n", ch.Guild, ch.Name, strings.Join(names, ", "))
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
