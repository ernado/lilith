package bot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ernado/lilith"
)

// fakeMemory implements lilith.Memory; Maintain delegates to a func so a test
// can observe the context it is invoked with.
type fakeMemory struct {
	maintain func(ctx context.Context, chatID int64, msg lilith.Message) error
}

func (f fakeMemory) Maintain(ctx context.Context, chatID int64, msg lilith.Message) error {
	return f.maintain(ctx, chatID, msg)
}

func (f fakeMemory) Notes(context.Context, int64) ([]lilith.ChatNote, error) {
	return nil, nil
}

// maintainNotes must run detached from the request context: cancelling the
// caller's context (as happens when the message handler returns) must not
// cancel the in-flight notes maintenance.
func TestMaintainNotes_SurvivesRequestCancellation(t *testing.T) {
	t.Parallel()

	entered := make(chan context.Context, 1)
	release := make(chan struct{})

	a := &App{
		memory: fakeMemory{
			maintain: func(ctx context.Context, _ int64, _ lilith.Message) error {
				entered <- ctx
				<-release

				return nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.maintainNotes(ctx, 1, lilith.Message{MessageID: 1})

	// Simulate the message handler returning and its context being cancelled
	// while maintenance is still in flight.
	cancel()

	var gotCtx context.Context
	select {
	case gotCtx = <-entered:
	case <-time.After(time.Second):
		t.Fatal("Maintain was not invoked")
	}

	require.NoError(t, gotCtx.Err(), "maintenance context must not be cancelled by request cancellation")
	close(release)
}
