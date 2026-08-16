package openai

import (
	"strings"
	"testing"
)

func TestParseSSEFraming(t *testing.T) {
	input := ": heartbeat\r\n" +
		"event: message\r\n" +
		"data: {\"choices\":\r\n" +
		"data: []}\r\n\r\n" +
		"id: ignored\n" +
		"unknown-field: ignored\n" +
		"data: [DONE]"
	var got []string
	err := parseSSE(strings.NewReader(input), func(data string) error {
		got = append(got, data)
		return nil
	})
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	want := []string{"{\"choices\":\n[]}", "[DONE]"}
	if len(got) != len(want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event %d = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestParseSSERejectsOversizedLineOrFrame(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "line", input: "data: " + strings.Repeat("x", maxSSEFrameBytes+1) + "\n\n"},
		{name: "multiline frame", input: "data: " + strings.Repeat("x", maxSSEFrameBytes/2) + "\ndata: " + strings.Repeat("y", maxSSEFrameBytes/2+1) + "\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := parseSSE(strings.NewReader(test.input), func(string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "exceed") {
				t.Fatalf("error = %v, want bounded-frame error", err)
			}
		})
	}
}
