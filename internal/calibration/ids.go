package calibration

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

func NewID() (string, error) {
	return newID(time.Now().UTC(), rand.Reader)
}

func newID(now time.Time, random io.Reader) (string, error) {
	suffix := make([]byte, 4)
	if _, err := io.ReadFull(random, suffix); err != nil {
		return "", fmt.Errorf("generate calibration ID: %w", err)
	}
	return now.UTC().Format("20060102T150405Z") + "-cal-" + hex.EncodeToString(suffix), nil
}
