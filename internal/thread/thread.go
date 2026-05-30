// Package thread implements the logical conversation-grouping ("threads") layer
// that the bot computes on top of raw Telegram messages. The functions here are
// pure: all required state (parent message, last same-author message, history)
// is passed in by the caller, which owns any database access.
//
// See THREADS.md for the design this package ports.
package thread

import (
	"sort"
	"time"

	"github.com/ernado/lilith"
)

const (
	// MaxGapSeconds is the maximum allowed gap between an incoming message and
	// the author's previous message for implicit same-author continuation.
	MaxGapSeconds = 180

	// MaxInterveningMessages is the lookback window (in messages) used when
	// searching for the author's previous message.
	MaxInterveningMessages = 6
)

// Info holds resolved logical-thread metadata for a message.
type Info struct {
	// ThreadID is the grouping key: the root message id of the thread.
	ThreadID int64
	// RootMessageID is the message_id that started the thread.
	RootMessageID int64
	// ParentMessageID is the message this one directly continues, if any.
	ParentMessageID *int64
	// Source labels how the thread was derived.
	Source string
}

// Apply writes the resolved thread fields onto msg.
func (i Info) Apply(msg *lilith.Message) {
	msg.ThreadID = lilith.T(i.ThreadID)
	msg.ThreadRootMessageID = lilith.T(i.RootMessageID)
	msg.ThreadParentMessageID = i.ParentMessageID
	msg.ThreadSource = lilith.T(i.Source)
}

// inheritRoot returns the root message id carried by parent, falling back to
// fallback when the parent has none.
func inheritRoot(parent *lilith.Message, fallback int64) int64 {
	if parent != nil && parent.ThreadRootMessageID != nil {
		return *parent.ThreadRootMessageID
	}

	return fallback
}

// inheritThreadID returns the thread id carried by parent, synthesizing one from
// root when the parent has none.
func inheritThreadID(parent *lilith.Message, root int64) int64 {
	if parent != nil && parent.ThreadID != nil {
		return *parent.ThreadID
	}

	return root
}

// ResolveIncoming derives thread metadata for an incoming user message.
//
//   - parent is the message that incoming replies to (nil if it is not a reply,
//     or the reply target is not stored).
//   - lastAuthor is the author's most recent prior message in the same Telegram
//     topic within the lookback window (nil if none qualifies).
func ResolveIncoming(incoming lilith.Message, parent, lastAuthor *lilith.Message) Info {
	// Strategy A — explicit reply.
	if incoming.ReplyToID != nil {
		replyID := *incoming.ReplyToID

		if parent != nil {
			root := inheritRoot(parent, replyID)

			return Info{
				ThreadID:        inheritThreadID(parent, root),
				RootMessageID:   root,
				ParentMessageID: &replyID,
				Source:          "explicit_reply",
			}
		}

		return Info{
			ThreadID:        replyID,
			RootMessageID:   replyID,
			ParentMessageID: &replyID,
			Source:          "explicit_reply_external",
		}
	}

	// Strategy B — implicit same-author continuation.
	if lastAuthor != nil && sameTopic(incoming.MessageThreadID, lastAuthor.MessageThreadID) {
		gap := incoming.Date.Sub(lastAuthor.Date)

		if gap >= 0 && gap <= MaxGapSeconds*time.Second {
			root := inheritRoot(lastAuthor, lastAuthor.MessageID)
			parentID := lastAuthor.MessageID

			return Info{
				ThreadID:        inheritThreadID(lastAuthor, root),
				RootMessageID:   root,
				ParentMessageID: &parentID,
				Source:          "implicit_same_author",
			}
		}
	}

	// Strategy C — new thread.
	return Info{
		ThreadID:      incoming.MessageID,
		RootMessageID: incoming.MessageID,
		Source:        "new_thread",
	}
}

// ResolveBotReply derives thread metadata for a message the bot just sent.
//
//   - botMessageID is the id of the message the bot sent.
//   - replyToID is the message the bot replied to (nil if it started fresh).
//   - parent is the reply target if it is known (nil if unstored).
func ResolveBotReply(botMessageID int64, replyToID *int64, parent *lilith.Message) Info {
	if replyToID != nil {
		root := inheritRoot(parent, *replyToID)

		source := "bot_parent_external"
		if parent != nil {
			source = "bot_parent"
		}

		return Info{
			ThreadID:        inheritThreadID(parent, root),
			RootMessageID:   root,
			ParentMessageID: replyToID,
			Source:          source,
		}
	}

	return Info{
		ThreadID:      botMessageID,
		RootMessageID: botMessageID,
		Source:        "bot_new",
	}
}

// sameTopic reports whether two Telegram topic ids refer to the same topic,
// treating two nil values as the same (non-forum) topic.
func sameTopic(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return *a == *b
}

// sameThread reports whether a and b belong to the same logical thread: by
// ThreadID when both have one, else by ThreadRootMessageID when both have one.
func sameThread(a, b lilith.Message) bool {
	if a.ThreadID != nil && b.ThreadID != nil {
		return *a.ThreadID == *b.ThreadID
	}

	if a.ThreadRootMessageID != nil && b.ThreadRootMessageID != nil {
		return *a.ThreadRootMessageID == *b.ThreadRootMessageID
	}

	return false
}

// hasThreadMetadata reports whether any message carries thread metadata.
func hasThreadMetadata(history []lilith.Message) bool {
	for i := range history {
		m := history[i]
		if m.ThreadID != nil || m.ThreadRootMessageID != nil ||
			m.ThreadParentMessageID != nil || m.ThreadSource != nil {
			return true
		}
	}

	return false
}

// SelectHistoryCandidates selects, from a history ordered oldest-first, the
// messages most relevant to the conversation around activeMessageID. It scopes
// selection to the active message's Telegram topic, prioritizes the active
// logical thread (up to ~70% of maxRootMessages), then fills with any remaining
// in-scope messages. The result is returned oldest-first.
//
// When no message carries thread metadata it falls back to the tail of history.
func SelectHistoryCandidates(history []lilith.Message, activeMessageID int64, maxRootMessages int) []lilith.Message {
	if maxRootMessages <= 0 || len(history) == 0 {
		return nil
	}

	if !hasThreadMetadata(history) {
		if len(history) > maxRootMessages {
			return history[len(history)-maxRootMessages:]
		}

		return history
	}

	anchor := history[findAnchor(history, activeMessageID)]
	scopedTopic := anchor.MessageThreadID

	inScope := func(m lilith.Message) bool {
		return sameTopic(scopedTopic, m.MessageThreadID)
	}

	seen := make(map[int64]bool, maxRootMessages)
	var selected []int

	// Active-thread pass (budgeted). When scoped to a Telegram topic, the topic
	// filter alone decides membership; otherwise sameThread is used.
	budget := maxRootMessages * 7 / 10
	if budget < 1 {
		budget = 1
	}

	for i := len(history) - 1; i >= 0 && len(selected) < budget; i-- {
		m := history[i]
		if seen[m.MessageID] || !inScope(m) {
			continue
		}

		member := scopedTopic != nil || sameThread(anchor, m)
		if !member {
			continue
		}

		seen[m.MessageID] = true
		selected = append(selected, i)
	}

	// General fill pass.
	for i := len(history) - 1; i >= 0 && len(selected) < maxRootMessages; i-- {
		m := history[i]
		if seen[m.MessageID] || !inScope(m) {
			continue
		}

		seen[m.MessageID] = true
		selected = append(selected, i)
	}

	sort.Ints(selected)

	out := make([]lilith.Message, 0, len(selected))
	for _, i := range selected {
		out = append(out, history[i])
	}

	return out
}

// findAnchor returns the index of the message with id activeMessageID, or the
// last non-bot message, or the last message.
func findAnchor(history []lilith.Message, activeMessageID int64) int {
	for i := range history {
		if history[i].MessageID == activeMessageID {
			return i
		}
	}

	for i := len(history) - 1; i >= 0; i-- {
		if !history[i].IsMyself {
			return i
		}
	}

	return len(history) - 1
}
