package thread

import (
	"testing"
	"time"

	"github.com/ernado/lilith"
)

func TestResolveIncoming_ExplicitReply(t *testing.T) {
	parent := &lilith.Message{
		MessageID:           10,
		ThreadID:            lilith.T(int64(5)),
		ThreadRootMessageID: lilith.T(int64(5)),
	}
	incoming := lilith.Message{MessageID: 20, ReplyToID: lilith.T(int64(10))}

	got := ResolveIncoming(incoming, parent, nil)

	if got.Source != "explicit_reply" {
		t.Fatalf("source = %q", got.Source)
	}
	if got.ThreadID != 5 {
		t.Fatalf("thread id = %d", got.ThreadID)
	}
	if got.RootMessageID != 5 {
		t.Fatalf("root = %d", got.RootMessageID)
	}
	if got.ParentMessageID == nil || *got.ParentMessageID != 10 {
		t.Fatalf("parent = %v", got.ParentMessageID)
	}
}

func TestResolveIncoming_ExplicitReplyExternal(t *testing.T) {
	incoming := lilith.Message{MessageID: 20, ReplyToID: lilith.T(int64(10))}

	got := ResolveIncoming(incoming, nil, nil)

	if got.Source != "explicit_reply_external" {
		t.Fatalf("source = %q", got.Source)
	}
	if got.ThreadID != 10 || got.RootMessageID != 10 {
		t.Fatalf("got = %+v", got)
	}
}

func TestResolveIncoming_ImplicitSameAuthor(t *testing.T) {
	base := time.Now()
	last := &lilith.Message{
		MessageID:           10,
		Date:                base,
		ThreadID:            lilith.T(int64(5)),
		ThreadRootMessageID: lilith.T(int64(5)),
	}
	incoming := lilith.Message{MessageID: 20, Date: base.Add(60 * time.Second)}

	got := ResolveIncoming(incoming, nil, last)

	if got.Source != "implicit_same_author" {
		t.Fatalf("source = %q", got.Source)
	}
	if got.ThreadID != 5 || got.RootMessageID != 5 {
		t.Fatalf("got = %+v", got)
	}
	if got.ParentMessageID == nil || *got.ParentMessageID != 10 {
		t.Fatalf("parent = %v", got.ParentMessageID)
	}
}

func TestResolveIncoming_ImplicitSameAuthor_TooOld(t *testing.T) {
	base := time.Now()
	last := &lilith.Message{MessageID: 10, Date: base}
	incoming := lilith.Message{MessageID: 20, Date: base.Add(200 * time.Second)}

	got := ResolveIncoming(incoming, nil, last)

	if got.Source != "new_thread" {
		t.Fatalf("source = %q", got.Source)
	}
}

func TestResolveIncoming_ImplicitSameAuthor_DifferentTopic(t *testing.T) {
	base := time.Now()
	last := &lilith.Message{MessageID: 10, Date: base, MessageThreadID: lilith.T(int64(1))}
	incoming := lilith.Message{MessageID: 20, Date: base.Add(10 * time.Second), MessageThreadID: lilith.T(int64(2))}

	got := ResolveIncoming(incoming, nil, last)

	if got.Source != "new_thread" {
		t.Fatalf("source = %q", got.Source)
	}
}

func TestResolveIncoming_NewThread(t *testing.T) {
	got := ResolveIncoming(lilith.Message{MessageID: 42}, nil, nil)

	if got.Source != "new_thread" || got.ThreadID != 42 || got.RootMessageID != 42 {
		t.Fatalf("got = %+v", got)
	}
	if got.ParentMessageID != nil {
		t.Fatalf("parent = %v", got.ParentMessageID)
	}
}

func TestResolveBotReply(t *testing.T) {
	parent := &lilith.Message{
		MessageID:           10,
		ThreadID:            lilith.T(int64(5)),
		ThreadRootMessageID: lilith.T(int64(5)),
	}

	got := ResolveBotReply(30, lilith.T(int64(10)), parent)
	if got.Source != "bot_parent" || got.ThreadID != 5 || got.RootMessageID != 5 {
		t.Fatalf("bot_parent: %+v", got)
	}

	got = ResolveBotReply(30, lilith.T(int64(10)), nil)
	if got.Source != "bot_parent_external" || got.ThreadID != 10 {
		t.Fatalf("bot_parent_external: %+v", got)
	}

	got = ResolveBotReply(30, nil, nil)
	if got.Source != "bot_new" || got.ThreadID != 30 || got.RootMessageID != 30 {
		t.Fatalf("bot_new: %+v", got)
	}
}

func TestSelectHistoryCandidatesV3_FallbackNoMetadata(t *testing.T) {
	history := []lilith.Message{
		{MessageID: 1}, {MessageID: 2}, {MessageID: 3},
	}

	got := SelectHistoryCandidates(history, 3, 2)
	if len(got) != 2 || got[0].MessageID != 2 || got[1].MessageID != 3 {
		t.Fatalf("got = %+v", got)
	}
}

func TestSelectHistoryCandidatesV3_PrioritizesActiveThread(t *testing.T) {
	tA := lilith.T(int64(100))
	tB := lilith.T(int64(200))
	history := []lilith.Message{
		{MessageID: 1, ThreadID: tA},
		{MessageID: 2, ThreadID: tB},
		{MessageID: 3, ThreadID: tA},
		{MessageID: 4, ThreadID: tB},
		{MessageID: 5, ThreadID: tA},
	}

	// Active message 5 is in thread:a. Budget (floor(3*0.7)=2) pulls the two
	// newest a-thread messages (5, 3) first; the general fill pass then adds the
	// newest remaining message (4), dropping the older a-thread message (1).
	got := SelectHistoryCandidates(history, 5, 3)

	ids := map[int64]bool{}
	for _, m := range got {
		ids[m.MessageID] = true
	}

	for _, want := range []int64{3, 5} {
		if !ids[want] {
			t.Fatalf("expected thread:a message %d in %+v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %+v", got)
	}
	if ids[1] {
		t.Fatalf("older a-thread message 1 should be dropped: %+v", got)
	}

	// Output must be ordered oldest-first.
	for i := 1; i < len(got); i++ {
		if got[i-1].MessageID > got[i].MessageID {
			t.Fatalf("not ordered: %+v", got)
		}
	}
}

func TestSelectHistoryCandidatesV3_TopicScope(t *testing.T) {
	topic := lilith.T(int64(7))
	history := []lilith.Message{
		{MessageID: 1, ThreadID: lilith.T(int64(11))},
		{MessageID: 2, ThreadID: lilith.T(int64(22)), MessageThreadID: topic},
		{MessageID: 3, ThreadID: lilith.T(int64(33)), MessageThreadID: topic},
	}

	// Anchor message 3 is in topic 7; out-of-topic message 1 must be excluded.
	got := SelectHistoryCandidates(history, 3, 10)

	for _, m := range got {
		if m.MessageID == 1 {
			t.Fatalf("out-of-topic message leaked: %+v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 in-topic messages, got %+v", got)
	}
}
