package exit

import (
	"testing"
	"time"
)

func TestNextBackoff(t *testing.T) {
	tests := []struct {
		name    string
		current time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{"double 1s", 1 * time.Second, 60 * time.Second, 2 * time.Second},
		{"double 5s", 5 * time.Second, 60 * time.Second, 10 * time.Second},
		{"double 30s", 30 * time.Second, 60 * time.Second, 60 * time.Second},
		{"cap at max", 40 * time.Second, 60 * time.Second, 60 * time.Second},
		{"already at max", 60 * time.Second, 60 * time.Second, 60 * time.Second},
		{"exceeds max", 100 * time.Second, 60 * time.Second, 60 * time.Second},
		{"small values", 100 * time.Millisecond, 1 * time.Second, 200 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextBackoff(tt.current, tt.max)
			if got != tt.want {
				t.Errorf("nextBackoff(%v, %v) = %v, want %v", tt.current, tt.max, got, tt.want)
			}
		})
	}
}
