package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

type sseEvent struct {
	Event string
	Data  []byte
	ID    string
	HasID bool
}

type SSECursor struct {
	LastEventID string
	Events      int
}

func decodeSSE(
	ctx context.Context,
	body io.Reader,
	consume func(sseEvent) (bool, error),
) (SSECursor, error) {
	var cursor SSECursor
	if body == nil || consume == nil {
		return cursor, errInvalidRPCResponse
	}

	reader := bufio.NewReaderSize(body, 64<<10)
	event := sseEvent{}
	data := make([][]byte, 0, 4)
	eventBytes := 0
	hasEventField := false

	reset := func() {
		event = sseEvent{}
		data = data[:0]
		eventBytes = 0
		hasEventField = false
	}
	dispatch := func() (bool, error) {
		if !hasEventField {
			reset()
			return false, nil
		}
		if cursor.Events >= maxSSEEvents {
			return false, ErrTooManySSEEvents
		}
		event.Data = bytes.Join(data, []byte{'\n'})
		cursor.Events++
		if event.HasID {
			cursor.LastEventID = event.ID
		}
		stop, err := consume(event)
		reset()
		return stop, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return cursor, err
		}

		line, readErr := readBoundedSSELine(reader)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if err := ctx.Err(); err != nil {
				return cursor, err
			}
			if errors.Is(readErr, ErrSSEEventTooLarge) {
				return cursor, ErrSSEEventTooLarge
			}
			return cursor, ErrSSEInterrupted
		}

		if len(line) > 0 || readErr == nil {
			if len(line) == 0 {
				stop, err := dispatch()
				if err != nil {
					return cursor, err
				}
				if stop {
					return cursor, nil
				}
			} else {
				eventBytes += len(line)
				if eventBytes > maxSSEEventBytes {
					return cursor, ErrSSEEventTooLarge
				}
				if line[0] != ':' {
					field, value := splitSSEField(line)
					switch string(field) {
					case "event":
						event.Event = string(value)
						hasEventField = true
					case "data":
						data = append(data, append([]byte(nil), value...))
						hasEventField = true
					case "id":
						id := string(value)
						if !validLastEventID(id) {
							return cursor, ErrInvalidLastEventID
						}
						event.ID = id
						event.HasID = true
						hasEventField = true
					}
				}
			}
		}

		if errors.Is(readErr, io.EOF) {
			if hasEventField {
				stop, err := dispatch()
				if err != nil {
					return cursor, err
				}
				if stop {
					return cursor, nil
				}
			}
			return cursor, ErrSSEInterrupted
		}
	}
}

func readBoundedSSELine(reader *bufio.Reader) ([]byte, error) {
	line := make([]byte, 0, 256)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxSSEEventBytes+2 {
			return nil, ErrSSEEventTooLarge
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}
		return line, err
	}
}

func splitSSEField(line []byte) ([]byte, []byte) {
	colon := bytes.IndexByte(line, ':')
	if colon < 0 {
		return line, nil
	}
	value := line[colon+1:]
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return line[:colon], value
}

func validLastEventID(value string) bool {
	return len(value) <= maxLastEventIDBytes &&
		utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n") &&
		httpguts.ValidHeaderFieldValue(value)
}
