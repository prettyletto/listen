//go:build linux

package playerui

import (
	"testing"
	"time"
)

func TestDecodeTerminalKeys(t *testing.T) {
	tests := []struct {
		name string
		seq  []byte
		want []string
	}{
		{name: "up arrow", seq: []byte{0x1b, '[', 'A'}, want: []string{"up"}},
		{name: "right arrow", seq: []byte{0x1b, '[', 'C'}, want: []string{"right"}},
		{name: "enter and controls", seq: []byte{'\r', ' ', 3, 'q'}, want: []string{"enter", "space", "ctrl+c", "q"}},
		{name: "escape split across reads", seq: []byte{0x1b, '[', 'B'}, want: []string{"down"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var escapeState uint8
			lastEscape := time.Now()
			var got []string
			for _, value := range test.seq {
				key, complete := decodeKey(value, &escapeState, &lastEscape)
				if complete {
					got = append(got, key)
				}
			}
			if len(got) != len(test.want) {
				t.Fatalf("decoded keys = %#v, want %#v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("decoded keys = %#v, want %#v", got, test.want)
				}
			}
		})
	}
}
