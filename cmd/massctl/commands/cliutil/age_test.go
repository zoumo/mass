package cliutil

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 3 * time.Minute, "3m"},
		{"hours", 5 * time.Hour, "5h"},
		{"days", 72 * time.Hour, "3d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAge(time.Now().Add(-tt.ago))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatAge_Zero(t *testing.T) {
	assert.Equal(t, "<unknown>", FormatAge(time.Time{}))
}
