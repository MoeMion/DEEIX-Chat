package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type failingSSEReader struct {
	err error
}

func (r failingSSEReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestDecodeSSECompleteEventsAndCursor(t *testing.T) {
	input := ": comment\r\n" +
		"event: message\r\n" +
		"id: event-1\r\n" +
		"data: first\r\n" +
		"data: second\r\n\r\n" +
		"id:\n" +
		"data: final"

	var events []sseEvent
	cursor, err := decodeSSE(context.Background(), strings.NewReader(input), func(event sseEvent) (bool, error) {
		events = append(events, event)
		return len(events) == 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	if events[0].Event != "message" || string(events[0].Data) != "first\nsecond" ||
		events[0].ID != "event-1" || !events[0].HasID {
		t.Fatalf("first event = %#v", events[0])
	}
	if string(events[1].Data) != "final" || events[1].ID != "" || !events[1].HasID {
		t.Fatalf("final event = %#v", events[1])
	}
	if cursor.LastEventID != "" || cursor.Events != 2 {
		t.Fatalf("cursor = %#v", cursor)
	}
}

func TestDecodeSSEEventAndCountBounds(t *testing.T) {
	t.Run("event exact limit", func(t *testing.T) {
		const prefix = "data: "
		input := prefix + strings.Repeat("x", maxSSEEventBytes-len(prefix)) + "\n\n"
		cursor, err := decodeSSE(context.Background(), strings.NewReader(input), func(sseEvent) (bool, error) {
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if cursor.Events != 1 {
			t.Fatalf("events = %d, want 1", cursor.Events)
		}
	})

	t.Run("event limit plus one", func(t *testing.T) {
		const prefix = "data: "
		input := prefix + strings.Repeat("x", maxSSEEventBytes-len(prefix)+1) + "\n\n"
		_, err := decodeSSE(context.Background(), strings.NewReader(input), func(sseEvent) (bool, error) {
			return true, nil
		})
		if !errors.Is(err, ErrSSEEventTooLarge) {
			t.Fatalf("error = %v, want ErrSSEEventTooLarge", err)
		}
	})

	t.Run("event count exact limit", func(t *testing.T) {
		input := strings.Repeat("data: {}\n\n", maxSSEEvents)
		cursor, err := decodeSSE(context.Background(), strings.NewReader(input), func(sseEvent) (bool, error) {
			return false, nil
		})
		if !errors.Is(err, ErrSSEInterrupted) {
			t.Fatalf("error = %v, want ErrSSEInterrupted", err)
		}
		if cursor.Events != maxSSEEvents {
			t.Fatalf("events = %d, want %d", cursor.Events, maxSSEEvents)
		}
	})

	t.Run("event count limit plus one", func(t *testing.T) {
		input := strings.Repeat("data: {}\n\n", maxSSEEvents+1)
		cursor, err := decodeSSE(context.Background(), strings.NewReader(input), func(sseEvent) (bool, error) {
			return false, nil
		})
		if !errors.Is(err, ErrTooManySSEEvents) {
			t.Fatalf("error = %v, want ErrTooManySSEEvents", err)
		}
		if cursor.Events != maxSSEEvents {
			t.Fatalf("events = %d, want %d", cursor.Events, maxSSEEvents)
		}
	})
}

func TestDecodeSSELastEventIDValidation(t *testing.T) {
	t.Run("exact limit", func(t *testing.T) {
		want := strings.Repeat("e", maxLastEventIDBytes)
		cursor, err := decodeSSE(context.Background(), strings.NewReader("id: "+want+"\ndata: {}\n\n"), func(sseEvent) (bool, error) {
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if cursor.LastEventID != want {
			t.Fatalf("event id length = %d, want %d", len(cursor.LastEventID), len(want))
		}
	})

	for _, tt := range []struct {
		name string
		id   string
	}{
		{name: "oversized", id: strings.Repeat("e", maxLastEventIDBytes+1)},
		{name: "invalid utf8", id: string([]byte{0xff})},
		{name: "nul", id: "bad\x00id"},
		{name: "carriage return", id: "bad\rid"},
		{name: "invalid header value", id: "bad\x7fid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeSSE(context.Background(), strings.NewReader("id: "+tt.id+"\ndata: {}\n\n"), func(sseEvent) (bool, error) {
				return true, nil
			})
			if !errors.Is(err, ErrInvalidLastEventID) {
				t.Fatalf("error = %v, want ErrInvalidLastEventID", err)
			}
		})
	}
}

func TestDecodeSSECancellationInterruptionAndConsumerError(t *testing.T) {
	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := decodeSSE(ctx, strings.NewReader("data: {}\n\n"), func(sseEvent) (bool, error) {
			return true, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("reader interruption keeps cursor", func(t *testing.T) {
		readErr := errors.New("reader failed")
		reader := io.MultiReader(
			strings.NewReader("id: event-7\ndata: {}\n\n"),
			failingSSEReader{err: readErr},
		)
		cursor, err := decodeSSE(context.Background(), reader, func(sseEvent) (bool, error) {
			return false, nil
		})
		if !errors.Is(err, ErrSSEInterrupted) || errors.Is(err, readErr) {
			t.Fatalf("error = %v, want sanitized ErrSSEInterrupted", err)
		}
		if cursor.LastEventID != "event-7" || cursor.Events != 1 {
			t.Fatalf("cursor = %#v", cursor)
		}
	})

	t.Run("consumer error", func(t *testing.T) {
		consumeErr := errors.New("consumer failed")
		_, err := decodeSSE(context.Background(), strings.NewReader("data: {}\n\n"), func(sseEvent) (bool, error) {
			return false, consumeErr
		})
		if !errors.Is(err, consumeErr) {
			t.Fatalf("error = %v, want consumer error", err)
		}
	})
}
