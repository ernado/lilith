package lilith

import "context"

// DiscordMember is a user currently present in a Discord voice channel.
type DiscordMember struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname,omitempty"`
}

// DiscordChannel is a Discord voice channel together with the members present
// in it right now.
type DiscordChannel struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Guild   string          `json:"guild,omitempty"`
	Members []DiscordMember `json:"members"`
}

//go:generate go tool moq -out internal/mock/discord_provider.go -pkg mock . DiscordProvider

// DiscordProvider reports which Discord voice channels are currently populated
// and by whom. It is used by the AI layer as a tool.
type DiscordProvider interface {
	// PopulatedChannels returns every voice channel that currently has at least
	// one member, with the members present in each.
	PopulatedChannels(ctx context.Context) ([]DiscordChannel, error)
}
