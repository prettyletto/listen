package argos

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelInstallerVerifiesAndCommitsRequiredFiles(t *testing.T) {
	archive := testModelArchive(t)
	digest := sha256.Sum256(archive)
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return modelResponse(request, archive), nil
	})}

	root := filepath.Join(t.TempDir(), "translation", ModelDirectory)
	installer := ModelInstaller{Root: root, URL: "https://model.invalid/test", SHA256: hex.EncodeToString(digest[:]), Client: client}
	if err := installer.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !modelInstalled(root, installer.SHA256) {
		t.Fatal("verified model was not installed")
	}
	for _, path := range []string{"sentencepiece.model", filepath.Join("model", "config.json"), filepath.Join("model", "model.bin"), filepath.Join("model", "shared_vocabulary.json")} {
		if info, err := os.Stat(filepath.Join(root, path)); err != nil || info.Size() == 0 {
			t.Fatalf("required file %q missing or empty: %v", path, err)
		}
	}
	if err := installer.Install(context.Background()); err != nil {
		t.Fatalf("repeat install: %v", err)
	}
	if got := requests; got != 1 {
		t.Fatalf("repeat setup made %d downloads, want 1", got)
	}
}

func TestModelInstallerRejectsBadDigestWithoutLeavingPartialInstall(t *testing.T) {
	archive := testModelArchive(t)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return modelResponse(request, archive), nil
	})}

	root := filepath.Join(t.TempDir(), "translation", ModelDirectory)
	err := (ModelInstaller{Root: root, URL: "https://model.invalid/test", SHA256: strings.Repeat("0", 64), Client: client}).Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("install error = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("failed install left a visible model directory: %v", err)
	}
}

func TestModelInstallerHonorsCancellation(t *testing.T) {
	started := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	root := filepath.Join(t.TempDir(), "translation", ModelDirectory)
	done := make(chan error, 1)
	go func() {
		done <- (ModelInstaller{Root: root, URL: "https://model.invalid/test", SHA256: ModelSHA256, Client: client}).Install(ctx)
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled model install succeeded")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cancelled install left a visible model directory: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func modelResponse(request *http.Request, archive []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(archive)),
		ContentLength: int64(len(archive)),
		Request:       request,
	}
}

func testModelArchive(t *testing.T) []byte {
	t.Helper()
	var buffer strings.Builder
	writer := zip.NewWriter(stringBuilderWriter{builder: &buffer})
	files := map[string]string{
		"metadata.json":                `{"package_version":"1.9","from_code":"en","to_code":"pb"}`,
		"sentencepiece.model":          "tokenizer",
		"model/config.json":            `{"add_source_bos":true}`,
		"model/model.bin":              "model weights",
		"model/shared_vocabulary.json": `{"hello": 0}`,
	}
	for name, body := range files {
		entry, err := writer.Create("translate-en_pb-1_9/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(buffer.String())
}

type stringBuilderWriter struct{ builder *strings.Builder }

func (writer stringBuilderWriter) Write(buffer []byte) (int, error) {
	return writer.builder.Write(buffer)
}
