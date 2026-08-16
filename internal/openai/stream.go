package openai

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxSSEFrameBytes = 1024 * 1024

var ErrUnexpectedEOF = errors.New("SSE stream ended before [DONE]")

type dataHandler func(string) error

// parseSSE reads standard SSE framing and dispatches joined data fields. It
// intentionally knows nothing about OpenAI JSON payloads.
func parseSSE(reader io.Reader, handle dataHandler) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxSSEFrameBytes)

	dataLines := make([]string, 0, 1)
	frameBytes := 0
	dispatch := func() error {
		if len(dataLines) == 0 {
			frameBytes = 0
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		frameBytes = 0
		return handle(data)
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		if strings.HasPrefix(value, " ") {
			value = value[1:]
		}

		switch field {
		case "data":
			frameBytes += len(value)
			if len(dataLines) > 0 {
				frameBytes++
			}
			if frameBytes > maxSSEFrameBytes {
				return fmt.Errorf("SSE frame exceeds %d bytes", maxSSEFrameBytes)
			}
			dataLines = append(dataLines, value)
		case "event", "id", "retry":
			// These fields are valid SSE metadata but are not used by the
			// OpenAI-compatible protocol.
		default:
			// The SSE specification requires unknown fields to be ignored.
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream (line or frame may exceed %d bytes): %w", maxSSEFrameBytes, err)
	}
	return dispatch()
}
