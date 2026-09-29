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

func TestMPVReadsCurrentSegmentDuration(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "duration" {
			return map[string]any{"error": "success", "data": float64(42.25)}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	duration, err := player.CurrentDuration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if duration != 42250*time.Millisecond {
		t.Fatalf("current segment duration = %s, want 42.25s", duration)
	}
	want := []any{[]any{"get_property", "duration"}}
	if got := requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mpv duration command = %#v, want %#v", got, want)
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

func TestMPVSeekByMovesAcrossReadySegmentsAndKeepsThePlaylist(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(0)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": float64(3)}
		}
		if command[0] == "get_property" && command[1] == "playlist/0/id" {
			return map[string]any{"error": "success", "data": float64(101)}
		}
		if command[0] == "get_property" && command[1] == "playlist/1/id" {
			return map[string]any{"error": "success", "data": float64(102)}
		}
		if command[0] == "playlist-play-index" {
			return map[string]any{
				"error": "success",
				"events": []map[string]any{
					{"event": "start-file", "playlist_entry_id": float64(101)},
					{"event": "file-loaded"},
					{"event": "start-file", "playlist_entry_id": float64(102)},
					{"event": "file-loaded"},
				},
			}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 10, Path: "first.wav", Duration: 4 * time.Second},
		{Seq: 11, Path: "second.wav", Duration: 5 * time.Second},
		{Seq: 12, Path: "third.wav", Duration: 6 * time.Second},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	position, err := player.SeekBy(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if position.Seq != 11 || position.Offset != 4*time.Second || position.Duration != 5*time.Second {
		t.Fatalf("seek result = %#v", position)
	}
	wantTail := []any{
		[]any{"get_property", "playlist-pos"},
		[]any{"get_property", "time-pos"},
		[]any{"get_property", "playlist-pos"},
		[]any{"get_property", "playlist/0/id"},
		[]any{"get_property", "playlist/1/id"},
		[]any{"playlist-play-index", float64(1)},
		[]any{"set_property", "time-pos", float64(4)},
	}
	got := requests()
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("seek commands = %#v, want tail %#v", got, wantTail)
	}
	seq, err := playerQueueSequence(player, 1)
	if err != nil || seq != 11 {
		t.Fatalf("playlist entry 1 maps to sequence %d, err %v", seq, err)
	}
	if got := len(player.items); got != 3 {
		t.Fatalf("buffered playlist contains %d items after seeking, want all 3", got)
	}
}

func TestMPVSeekByReportsWhenForwardAudioIsNotBuffered(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(0)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": float64(3)}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	if err := player.Append(context.Background(), Item{Seq: 4, Path: "ready.wav", Duration: 4 * time.Second}); err != nil {
		t.Fatal(err)
	}
	_, err := player.SeekBy(context.Background(), 5*time.Second)
	if !errors.Is(err, ErrSeekNotBuffered) {
		t.Fatalf("seek error = %v, want ErrSeekNotBuffered", err)
	}
	if got := requests(); len(got) != 4 {
		t.Fatalf("seek changed playback before target was ready: commands=%#v", got)
	}
}

func TestMPVSeekByRetriesWhenPlaylistChangesDuringPositionRead(t *testing.T) {
	var playlistReads, timeReads int
	player, _, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			playlistReads++
			position := 1.0
			if playlistReads == 1 {
				position = 0
			}
			return map[string]any{"error": "success", "data": position}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			timeReads++
			position := 1.0
			if timeReads == 1 {
				position = 3
			}
			return map[string]any{"error": "success", "data": position}
		}
		if command[0] == "get_property" && command[1] == "playlist/1/id" {
			return map[string]any{"error": "success", "data": float64(102)}
		}
		if command[0] == "playlist-play-index" {
			return map[string]any{
				"error": "success",
				"events": []map[string]any{
					{"event": "start-file", "playlist_entry_id": float64(102)},
					{"event": "file-loaded"},
				},
			}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 10, Path: "first.wav", Duration: 4 * time.Second},
		{Seq: 11, Path: "second.wav", Duration: 5 * time.Second},
		{Seq: 12, Path: "third.wav", Duration: 6 * time.Second},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	position, err := player.SeekBy(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if position.Seq != 11 || position.Offset != 2*time.Second {
		t.Fatalf("seek position = %#v, want sequence 11 at 2s after retry", position)
	}
	if playlistReads != 4 || timeReads != 2 {
		t.Fatalf("position snapshots read playlist=%d times=%d; want 4 and 2", playlistReads, timeReads)
	}
}

func TestMPVSeekByWaitForTargetHonorsCancellation(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(0)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": float64(3)}
		}
		if command[0] == "get_property" && command[1] == "playlist/1/id" {
			return map[string]any{"error": "success", "data": float64(102)}
		}
		if command[0] == "get_property" && command[1] == "playlist/0/id" {
			return map[string]any{"error": "success", "data": float64(101)}
		}
		if command[0] == "playlist-play-index" {
			if command[1] == float64(1) {
				return map[string]any{"error": "success"}
			}
			return map[string]any{
				"error": "success",
				// A delayed file-loaded event from the earlier load must not
				// satisfy the wait for this new selection.
				"events": []map[string]any{
					{"event": "start-file", "playlist_entry_id": float64(101)},
					{"event": "file-loaded"},
				},
			}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 10, Path: "first.wav", Duration: 4 * time.Second},
		{Seq: 11, Path: "second.wav", Duration: 5 * time.Second},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	player.loadingID = 102
	player.loadedID = 102
	player.loadGeneration = 1
	player.loadedGeneration = 1
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := player.SeekBy(ctx, 5*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("seek error = %v, want context deadline", err)
	}
	commands := requests()
	for _, command := range commands {
		if reflect.DeepEqual(command, []any{"set_property", "time-pos", float64(4)}) {
			t.Fatal("seek offset was applied before the newly selected entry loaded")
		}
	}
	wantRecovery := []any{
		[]any{"playlist-play-index", float64(0)},
		[]any{"set_property", "time-pos", float64(3)},
	}
	if !reflect.DeepEqual(commands[len(commands)-2:], wantRecovery) {
		t.Fatalf("failed seek recovery commands = %#v, want %#v", commands[len(commands)-2:], wantRecovery)
	}
}

func TestMPVSeekByRestoresPreviousPositionWhenTargetOffsetFails(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(0)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": float64(3)}
		}
		if command[0] == "get_property" && command[1] == "playlist/0/id" {
			return map[string]any{"error": "success", "data": float64(101)}
		}
		if command[0] == "get_property" && command[1] == "playlist/1/id" {
			return map[string]any{"error": "success", "data": float64(102)}
		}
		if command[0] == "playlist-play-index" {
			entryID := float64(102)
			if command[1] == float64(0) {
				entryID = 101
			}
			return map[string]any{
				"error": "success",
				"events": []map[string]any{
					{"event": "start-file", "playlist_entry_id": entryID},
					{"event": "file-loaded"},
				},
			}
		}
		if command[0] == "set_property" && command[1] == "time-pos" && command[2] == float64(4) {
			return map[string]any{"error": "command failed"}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 10, Path: "first.wav", Duration: 4 * time.Second},
		{Seq: 11, Path: "second.wav", Duration: 5 * time.Second},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := player.SeekBy(context.Background(), 5*time.Second); err == nil {
		t.Fatal("seek succeeded after mpv rejected the target offset")
	}
	wantTail := []any{
		[]any{"playlist-play-index", float64(1)},
		[]any{"set_property", "time-pos", float64(4)},
		[]any{"playlist-play-index", float64(0)},
		[]any{"set_property", "time-pos", float64(3)},
	}
	got := requests()
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("failed offset recovery commands = %#v, want %#v", got[len(got)-len(wantTail):], wantTail)
	}
}

func TestMPVSeekByMovesBackAcrossReadySegments(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		if command[0] == "get_property" && command[1] == "playlist-pos" {
			return map[string]any{"error": "success", "data": float64(1)}
		}
		if command[0] == "get_property" && command[1] == "time-pos" {
			return map[string]any{"error": "success", "data": float64(1)}
		}
		if command[0] == "get_property" && command[1] == "playlist/0/id" {
			return map[string]any{"error": "success", "data": float64(101)}
		}
		if command[0] == "get_property" && command[1] == "playlist/1/id" {
			return map[string]any{"error": "success", "data": float64(102)}
		}
		if command[0] == "playlist-play-index" {
			return map[string]any{
				"error": "success",
				"events": []map[string]any{
					{"event": "start-file", "playlist_entry_id": float64(101)},
					{"event": "file-loaded"},
				},
			}
		}
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	for _, item := range []Item{
		{Seq: 10, Path: "first.wav", Duration: 4 * time.Second},
		{Seq: 11, Path: "second.wav", Duration: 5 * time.Second},
		{Seq: 12, Path: "third.wav", Duration: 6 * time.Second},
	} {
		if err := player.Append(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	position, err := player.SeekBy(context.Background(), -5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if position.Seq != 10 || position.Offset != 0 || position.Duration != 4*time.Second {
		t.Fatalf("seek result = %#v, want first ready item at its beginning", position)
	}
	wantTail := []any{
		[]any{"get_property", "playlist-pos"},
		[]any{"get_property", "time-pos"},
		[]any{"get_property", "playlist-pos"},
		[]any{"get_property", "playlist/1/id"},
		[]any{"get_property", "playlist/0/id"},
		[]any{"playlist-play-index", float64(0)},
		[]any{"set_property", "time-pos", float64(0)},
	}
	got := requests()
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("seek commands = %#v, want tail %#v", got, wantTail)
	}
	seq, err := playerQueueSequence(player, 0)
	if err != nil || seq != 10 {
		t.Fatalf("playlist entry 0 maps to sequence %d, err %v", seq, err)
	}
}

func playerQueueSequence(player *MPV, index int) (uint64, error) {
	player.mu.Lock()
	defer player.mu.Unlock()
	if index < 0 || index >= len(player.seqs) {
		return 0, errors.New("playlist index is outside the queue")
	}
	return player.seqs[index], nil
}

func TestMPVPauseControlSetsPlaybackState(t *testing.T) {
	player, requests, closePeer := newIPCPlayer(t, func(command []any) any {
		return map[string]any{"error": "success"}
	})
	defer closePeer()
	if err := player.SetPaused(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := player.SetPaused(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := []any{[]any{"set_property", "pause", true}, []any{"set_property", "pause", false}}
	if got := requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("mpv pause commands = %#v, want %#v", got, want)
	}
	if err := player.SetPaused(nil, true); err == nil {
		t.Fatal("accepted a nil pause context")
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
			resultMap := result.(map[string]any)
			events, _ := resultMap["events"].([]map[string]any)
			delete(resultMap, "events")
			message := map[string]any{"request_id": request.RequestID}
			for key, value := range resultMap {
				message[key] = value
			}
			data, _ := json.Marshal(message)
			if _, err := peer.Write(append(data, '\n')); err != nil {
				return
			}
			for _, event := range events {
				data, _ := json.Marshal(event)
				if _, err := peer.Write(append(data, '\n')); err != nil {
					return
				}
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
