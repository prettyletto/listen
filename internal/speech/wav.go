package speech

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const expectedSampleRate = 24000

// ValidateWAV checks the PCM format produced by the Kokoro worker and returns
// its duration. It rejects empty, truncated, and non-mono-16-bit files.
func ValidateWAV(path string) (time.Duration, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var head [12]byte
	if _, err := io.ReadFull(file, head[:]); err != nil {
		return 0, fmt.Errorf("read WAV header: %w", err)
	}
	if string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, errors.New("audio is not a RIFF/WAVE file")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspect WAV file: %w", err)
	}
	riffSize := int64(binary.LittleEndian.Uint32(head[4:8]))
	if riffSize < 4 {
		return 0, errors.New("WAV RIFF container is too small")
	}
	riffEnd := int64(8) + riffSize
	if riffEnd > fileInfo.Size() {
		return 0, errors.New("WAV RIFF container is truncated")
	}
	remaining := riffSize - 4 // The WAVE form type is included in RIFF size.
	var channels, sampleRate, byteRate, bitsPerSample uint32
	var blockAlign uint16
	var dataSize uint32
	fmtFound, dataFound := false, false
	for {
		if remaining < 8 {
			if remaining != 0 {
				return 0, errors.New("WAV contains a truncated chunk header")
			}
			break
		}
		var chunkHead [8]byte
		if _, err := io.ReadFull(file, chunkHead[:]); err != nil {
			return 0, fmt.Errorf("read WAV chunk header: %w", err)
		}
		chunkSize := binary.LittleEndian.Uint32(chunkHead[4:8])
		chunkBytes := int64(chunkSize) + int64(chunkSize%2)
		remaining -= 8
		if chunkBytes > remaining {
			return 0, errors.New("WAV chunk is truncated")
		}
		switch string(chunkHead[:4]) {
		case "fmt ":
			if chunkSize < 16 {
				return 0, errors.New("WAV format chunk is too small")
			}
			var format [16]byte
			if _, err := io.ReadFull(file, format[:]); err != nil {
				return 0, fmt.Errorf("read WAV format: %w", err)
			}
			if binary.LittleEndian.Uint16(format[:2]) != 1 {
				return 0, errors.New("WAV audio is not uncompressed PCM")
			}
			channels = uint32(binary.LittleEndian.Uint16(format[2:4]))
			sampleRate = binary.LittleEndian.Uint32(format[4:8])
			byteRate = binary.LittleEndian.Uint32(format[8:12])
			blockAlign = binary.LittleEndian.Uint16(format[12:14])
			bitsPerSample = uint32(binary.LittleEndian.Uint16(format[14:16]))
			if _, err := io.CopyN(io.Discard, file, chunkBytes-16); err != nil {
				return 0, fmt.Errorf("skip WAV format extension: %w", err)
			}
			fmtFound = true
		case "data":
			dataSize = chunkSize
			dataFound = true
			if _, err := io.CopyN(io.Discard, file, chunkBytes); err != nil {
				return 0, fmt.Errorf("read WAV audio data: %w", err)
			}
		default:
			if _, err := io.CopyN(io.Discard, file, chunkBytes); err != nil {
				return 0, fmt.Errorf("skip WAV chunk: %w", err)
			}
		}
		remaining -= chunkBytes
		if fmtFound && dataFound {
			break
		}
	}
	if !fmtFound || !dataFound {
		return 0, errors.New("WAV is missing its format or audio data chunk")
	}
	if channels != 1 || sampleRate != expectedSampleRate || bitsPerSample != 16 || byteRate == 0 {
		return 0, fmt.Errorf("unsupported WAV format: channels=%d rate=%d bits=%d", channels, sampleRate, bitsPerSample)
	}
	if blockAlign != 2 || byteRate != sampleRate*uint32(blockAlign) {
		return 0, errors.New("WAV format has inconsistent sample alignment or byte rate")
	}
	if dataSize == 0 {
		return 0, errors.New("WAV contains no PCM samples")
	}
	if dataSize%uint32(blockAlign) != 0 {
		return 0, errors.New("WAV audio data ends with a partial PCM sample")
	}
	return time.Duration(uint64(dataSize) * uint64(time.Second) / uint64(byteRate)), nil
}
