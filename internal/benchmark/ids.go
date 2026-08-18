package benchmark

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

func NewRunID() (string, error) {
	return newRunID(time.Now().UTC(), rand.Reader)
}

func newRunID(now time.Time, random io.Reader) (string, error) {
	suffix := make([]byte, 4)
	if _, err := io.ReadFull(random, suffix); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}

func RequestID(sequence int) (string, error) {
	if sequence <= 0 {
		return "", fmt.Errorf("request sequence must be greater than zero")
	}
	return fmt.Sprintf("req-%06d", sequence), nil
}

func WarmupRequestID(sequence int) (string, error) {
	if sequence <= 0 {
		return "", fmt.Errorf("warmup request sequence must be greater than zero")
	}
	return fmt.Sprintf("warmup-%06d", sequence), nil
}
