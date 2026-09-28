package playback

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMPVAppendsFilesInSequence(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 0, Path: "/tmp/first segment.wav"},
		{Seq: 1, Path: "/tmp/second.wav"},
		{Seq: 2, Path: "/tmp/third.wav"},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	want := []any{
		[]any{"loadfile", "/tmp/first segment.wav", "replace"},
		[]any{"loadfile", "/tmp/second.wav", "append"},
		[]any{"loadfile", "/tmp/third.wav", "append"},
	}
	if got := requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mpv commands = %#v, want %#v", got, want)
	}
}

func TestMPVAppendAtAppliesResumeOffsetDuringLoad(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	offset := 12*time.Second + 500*time.Millisecond
	if err := player.AppendAt(context.Background(), Item{Seq: 7, Path: "/tmp/resume segment.wav"}, offset); err != nil {
		t.Fatal(err)
	}
	want := []any{"loadfile", "/tmp/resume segment.wav", "replace", float64(-1), map[string]any{"start": "12.5"}}
	if got := requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("mpv commands = %#v, want %#v", got, want)
	}
	if err := player.AppendAt(context.Background(), Item{Seq: 8, Path: "next.wav"}, -time.Second); err == nil {
		t.Fatal("accepted a negative resume offset")
	}
}

func TestMPVPositionMapsPlaylistIndexToSegmentAndOffset(t *testing.T) {
	player, _, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(1)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": 12.5}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, seq := range []uint64{7, 9} {
		if err := player.Append(context.Background(), Item{Seq: seq, Path: "segment.wav"}); err != nil {
			t.Fatal(err)
		}
	}
	seq, offset, err := player.Position(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if seq != 9 || offset != 12500*time.Millisecond {
		t.Fatalf("position = %d at %s", seq, offset)
	}
}

func TestMPVSeekSetsCurrentItemOffset(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	if err := player.Seek(context.Background(), 12*time.Second+500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := []any{"set_property", "time-pos", float64(12.5)}
	if got := requests(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("mpv commands = %#v, want %#v", got, want)
	}
	if err := player.Seek(context.Background(), -time.Second); err == nil {
		t.Fatal("accepted a negative seek offset")
	}
}

func TestMPVCommandHonorsCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		want error
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{
			name: "deadline",
			want: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
		},
		{
			name: "explicit cancel",
			want: context.Canceled,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					time.Sleep(20 * time.Millisecond)
					cancel()
				}()
				return ctx, cancel
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			player, _, closePeer := newIPCPlayer(t, nil)
			defer closePeer()
			ctx, cancel := test.ctx()
			defer cancel()
			err := player.Append(ctx, Item{Seq: 0, Path: "segment.wav"})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestMPVWaitHonorsContextCancellation(t *testing.T) {
	player := &MPV{done: make(chan struct{}), started: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := player.Wait(ctx); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestMPVWaitReturnsAfterQueuedPlaybackBecomesIdle(t *testing.T) {
	var idleChecks atomic.Int32
	player, _, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "idle-active" {
			return map[string]any{"error": "success", "data": idleChecks.Add(1) >= 2}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	player.done = make(chan struct{})
	if err := player.Append(context.Background(), Item{Seq: 0, Path: "finished.wav"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := player.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if idleChecks.Load() < 2 {
		t.Fatalf("Wait returned before the queued audio completed: idle checks=%d", idleChecks.Load())
	}
}

func TestMPVWaitReportsEarlyProcessExit(t *testing.T) {
	player, _, closePeer := newIPCPlayer(t, func(command []any) any {
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	if err := player.Append(context.Background(), Item{Seq: 0, Path: "unfinished.wav"}); err != nil {
		t.Fatal(err)
	}
	player.done = make(chan struct{})
	close(player.done)
	err := player.Wait(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited before playback completed") {
		t.Fatalf("got %v, want early-exit error", err)
	}
}

func newIPCPlayer(t *testing.T, response func([]any) any) (*MPV, func() []any, func()) {
	t.Helper()
	client, peer := net.Pipe()
	player := &MPV{conn: client, reader: bufio.NewReader(client), started: true}
	var got []any
	var gotMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer peer.Close()
		reader := bufio.NewReader(peer)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				Command   []any  `json:"command"`
				RequestID uint64 `json:"request_id"`
			}
			if err := json.Unmarshal(line, &request); err != nil {
				return
			}
			gotMu.Lock()
			got = append(got, request.Command)
			gotMu.Unlock()
			if response == nil {
				continue
			}
			result := response(request.Command)
			message := map[string]any{"request_id": request.RequestID}
			for key, value := range result.(map[string]any) {
				message[key] = value
			}
			data, _ := json.Marshal(message)
			if _, err := peer.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	closePeer := func() {
		_ = client.Close()
		<-done
	}
	return player, func() []any {
		gotMu.Lock()
		defer gotMu.Unlock()
		return append([]any(nil), got...)
	}, closePeer
}
