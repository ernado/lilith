// Package discord reports presence in Discord voice channels via the Discord
// gateway. It implements lilith.DiscordProvider, used by the AI layer as a tool.
package discord

import (
	"context"
	"fmt"
	"sort"

	"github.com/bwmarrin/discordgo"
	"github.com/go-faster/errors"

	"github.com/ernado/lilith"
)

var _ lilith.DiscordProvider = (*Client)(nil)

// Client maintains an open Discord gateway session and reports which voice
// channels are currently populated.
type Client struct {
	session *discordgo.Session
}

// New returns a Client for the given bot token. Call Open before use and Close
// when done.
func New(token string) (*Client, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.Wrap(err, "create session")
	}

	// Voice states tell us who is in which channel; guilds give us channel and
	// member metadata. Guild members is privileged and resolves nicknames.
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMembers

	return &Client{session: session}, nil
}

// Open connects to the Discord gateway and starts maintaining state.
func (c *Client) Open(context.Context) error {
	if err := c.session.Open(); err != nil {
		return errors.Wrap(err, "open session")
	}

	return nil
}

// Close tears down the gateway connection.
func (c *Client) Close() error {
	if err := c.session.Close(); err != nil {
		return errors.Wrap(err, "close session")
	}

	return nil
}

// PopulatedChannels returns every voice channel that currently has at least one
// member, across all guilds the bot is in, with the members present in each.
func (c *Client) PopulatedChannels(context.Context) ([]lilith.DiscordChannel, error) {
	st := c.session.State

	// Snapshot guild ids under the read lock, then resolve details through the
	// locking helpers to avoid holding the lock (and re-entering it) while we
	// look channels and members up.
	st.RLock()
	guildIDs := make([]string, 0, len(st.Guilds))
	for _, g := range st.Guilds {
		guildIDs = append(guildIDs, g.ID)
	}
	st.RUnlock()

	var channels []lilith.DiscordChannel
	for _, guildID := range guildIDs {
		guild, err := st.Guild(guildID)
		if err != nil {
			continue
		}

		st.RLock()
		voiceStates := append([]*discordgo.VoiceState(nil), guild.VoiceStates...)
		guildName := guild.Name
		st.RUnlock()

		byChannel := make(map[string][]lilith.DiscordMember)
		for _, vs := range voiceStates {
			byChannel[vs.ChannelID] = append(byChannel[vs.ChannelID], c.resolveMember(guildID, vs))
		}

		for channelID, members := range byChannel {
			channels = append(channels, lilith.DiscordChannel{
				ID:      channelID,
				Name:    c.channelName(channelID),
				Guild:   guildName,
				Members: members,
			})
		}
	}

	sort.Slice(channels, func(i, j int) bool {
		if channels[i].Guild != channels[j].Guild {
			return channels[i].Guild < channels[j].Guild
		}

		return channels[i].Name < channels[j].Name
	})

	return channels, nil
}

// Stats is a diagnostic snapshot of the cached gateway state.
type Stats struct {
	// Guilds is the number of guilds the bot is currently in.
	Guilds int
	// VoiceStates is the total number of users in voice across all guilds.
	VoiceStates int
}

// Stats returns a diagnostic snapshot of the cached gateway state. It is useful
// to tell apart "bot is in no guild", "guild present but nobody in voice" and a
// healthy populated state.
func (c *Client) Stats() Stats {
	st := c.session.State

	st.RLock()
	defer st.RUnlock()

	s := Stats{Guilds: len(st.Guilds)}
	for _, g := range st.Guilds {
		s.VoiceStates += len(g.VoiceStates)
	}

	return s
}

// Self returns the bot's own identity ("username (id)") once the gateway READY
// has been received, reporting false until then. It confirms which bot
// application a token belongs to.
func (c *Client) Self() (string, bool) {
	st := c.session.State

	st.RLock()
	defer st.RUnlock()

	if st.User == nil {
		return "", false
	}

	return fmt.Sprintf("%s (%s)", st.User.Username, st.User.ID), true
}

// resolveMember turns a voice state into a domain member, preferring the cached
// guild member (which carries the nickname) and falling back to the voice
// state's own member, then to the bare user id.
func (c *Client) resolveMember(guildID string, vs *discordgo.VoiceState) lilith.DiscordMember {
	m := lilith.DiscordMember{ID: vs.UserID}

	if member, err := c.session.State.Member(guildID, vs.UserID); err == nil && member != nil {
		m.Nickname = member.Nick
		if member.User != nil {
			m.Username = member.User.Username
		}

		return m
	}

	if vs.Member != nil {
		m.Nickname = vs.Member.Nick
		if vs.Member.User != nil {
			m.Username = vs.Member.User.Username
		}
	}

	return m
}

// channelName resolves a channel name from state, falling back to the id when
// the channel is not cached.
func (c *Client) channelName(channelID string) string {
	if ch, err := c.session.State.Channel(channelID); err == nil && ch != nil {
		return ch.Name
	}

	return channelID
}
