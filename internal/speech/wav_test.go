package speech

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWAVRejectsTruncatedAndInconsistentPCM(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "truncated RIFF payload",
			setup: func(t *testing.T, path string) {
				writeTestWAV(t, path, 24000)
				if err := os.Truncate(path, 51); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "truncated chunk header",
			setup: func(t *testing.T, path string) {
				data := make([]byte, 16)
				copy(data[:4], "RIFF")
				binary.LittleEndian.PutUint32(data[4:8], 8)
				copy(data[8:12], "WAVE")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "byte rate disagrees with PCM layout",
			setup: func(t *testing.T, path string) {
				writeTestWAV(t, path, 24000)
				writeWAVField(t, path, 28, 96000, 4)
			},
		},
		{
			name: "invalid block alignment",
			setup: func(t *testing.T, path string) {
				writeTestWAV(t, path, 24000)
				writeWAVField(t, path, 32, 1, 2)
			},
		},
		{
			name: "partial PCM sample",
			setup: func(t *testing.T, path string) {
				writeTestWAV(t, path, 24000)
				// Keep the original padded chunk length but declare an odd data size.
				writeWAVField(t, path, 40, 47999, 4)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.wav")
			test.setup(t, path)
			if _, err := ValidateWAV(path); err == nil {
				t.Fatal("accepted malformed WAV file")
			}
		})
	}
}

func writeWAVField(t *testing.T, path string, offset int64, value uint32, width int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	var data [4]byte
	if width == 2 {
		binary.LittleEndian.PutUint16(data[:2], uint16(value))
	} else {
		binary.LittleEndian.PutUint32(data[:], value)
	}
	if _, err := file.WriteAt(data[:width], offset); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
