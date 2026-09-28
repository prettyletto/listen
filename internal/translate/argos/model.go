package argos

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/paths"
)

const (
	BackendID       = "argos-opus-en-pb-1.9-ct2-4.8.2"
	LanguagePair    = "en:pt-BR"
	ModelVersion    = "1.9"
	ModelURL        = "https://argos-net.com/v1/translate-en_pb-1_9.argosmodel"
	ModelSHA256     = "1d1cd5e9540c6b38c258bed002a42d3b311b8a189acb74feaa311ef30d175c5b"
	ModelDirectory  = "argos-opus-en-pb-1.9"
	maxArchiveBytes = 96 << 20
	maxExpandedSize = 120 << 20
)

var installMu sync.Mutex

type ModelInstaller struct {
	Root   string
	URL    string
	SHA256 string
	Client *http.Client
}

type installedModel struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type modelMetadata struct {
	PackageVersion string `json:"package_version"`
	FromCode       string `json:"from_code"`
	ToCode         string `json:"to_code"`
}

func ModelRoot(userPaths paths.Paths) string {
	return filepath.Join(userPaths.DataDir, "translation", ModelDirectory)
}

func InstallModel(ctx context.Context, userPaths paths.Paths) error {
	if userPaths.DataDir == "" {
		return errors.New("translation setup requires the XDG data directory")
	}
	return (ModelInstaller{Root: ModelRoot(userPaths)}).Install(ctx)
}

// Install downloads only the local model artifact, verifies its pinned digest,
// validates the archive, and commits the required model files as one directory.
func (installer ModelInstaller) Install(ctx context.Context) error {
	if ctx == nil {
		return errors.New("translation model setup requires a context")
	}
	if installer.Root == "" {
		return errors.New("translation model setup requires an install directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.Clean(installer.Root)
	url := installer.URL
	if url == "" {
		url = ModelURL
	}
	expectedHash := strings.ToLower(installer.SHA256)
	if expectedHash == "" {
		expectedHash = ModelSHA256
	}
	if len(expectedHash) != sha256.Size*2 {
		return errors.New("translation model digest is invalid")
	}
	for _, char := range expectedHash {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return errors.New("translation model digest is invalid")
		}
	}

	installMu.Lock()
	defer installMu.Unlock()
	if modelInstalled(root, expectedHash) {
		return nil
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create translation model directory: %w", err)
	}
	archiveFile, err := os.CreateTemp(parent, ".argos-model-*.partial")
	if err != nil {
		return fmt.Errorf("prepare translation model download: %w", err)
	}
	archivePath := archiveFile.Name()
	defer os.Remove(archivePath)
	if err := installer.download(ctx, url, archiveFile); err != nil {
		_ = archiveFile.Close()
		return err
	}
	if err := archiveFile.Sync(); err != nil {
		_ = archiveFile.Close()
		return fmt.Errorf("sync translation model download: %w", err)
	}
	if err := archiveFile.Close(); err != nil {
		return fmt.Errorf("close translation model download: %w", err)
	}
	hash, err := hashFile(archivePath)
	if err != nil {
		return fmt.Errorf("verify translation model download: %w", err)
	}
	if hash != expectedHash {
		return fmt.Errorf("translation model checksum mismatch: expected %s, received %s", expectedHash, hash)
	}
	stage, err := os.MkdirTemp(parent, ".argos-model-install-*")
	if err != nil {
		return fmt.Errorf("prepare translation model install: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := extractModel(ctx, archivePath, stage); err != nil {
		return err
	}
	marker, _ := json.Marshal(installedModel{Version: ModelVersion, SHA256: expectedHash})
	if err := os.WriteFile(filepath.Join(stage, ".listen-model.json"), append(marker, '\n'), 0o600); err != nil {
		return fmt.Errorf("write translation model manifest: %w", err)
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	backup := root + ".old"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(root); err == nil {
		if err := os.Rename(root, backup); err != nil {
			return fmt.Errorf("replace incomplete translation model: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect translation model directory: %w", err)
	}
	if err := os.Rename(stage, root); err != nil {
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, root)
		}
		return fmt.Errorf("commit translation model install: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

func (installer ModelInstaller) download(ctx context.Context, url string, destination io.Writer) error {
	client := installer.Client
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Minute}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("prepare translation model download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("download local translation model: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download local translation model: server returned %s", response.Status)
	}
	if response.ContentLength > maxArchiveBytes {
		return fmt.Errorf("local translation model is larger than the allowed %d bytes", maxArchiveBytes)
	}
	count, err := io.Copy(destination, io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("save local translation model: %w", err)
	}
	if count > maxArchiveBytes {
		return fmt.Errorf("local translation model is larger than the allowed %d bytes", maxArchiveBytes)
	}
	return ctx.Err()
}

func ModelInstalled(root string) bool {
	return modelInstalled(root, ModelSHA256)
}

func modelInstalled(root, expectedHash string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".listen-model.json"))
	if err != nil {
		return false
	}
	var marker installedModel
	if json.Unmarshal(data, &marker) != nil || marker.Version != ModelVersion || marker.SHA256 != expectedHash {
		return false
	}
	for _, name := range []string{"sentencepiece.model", filepath.Join("model", "config.json"), filepath.Join("model", "model.bin"), filepath.Join("model", "shared_vocabulary.json")} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	return true
}

func extractModel(ctx context.Context, archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open local translation model archive: %w", err)
	}
	defer reader.Close()
	const prefix = "translate-en_pb-1_9/"
	allowed := map[string]string{
		"metadata.json":                "metadata.json",
		"sentencepiece.model":          "sentencepiece.model",
		"model/config.json":            filepath.Join("model", "config.json"),
		"model/model.bin":              filepath.Join("model", "model.bin"),
		"model/shared_vocabulary.json": filepath.Join("model", "shared_vocabulary.json"),
	}
	var expanded int64
	found := make(map[string]bool, len(allowed))
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		name := filepath.ToSlash(file.Name)
		if !strings.HasPrefix(name, prefix) {
			return fmt.Errorf("translation model archive contains an unexpected path %q", name)
		}
		relative := strings.TrimPrefix(name, prefix)
		targetRelative, ok := allowed[relative]
		if !ok {
			continue
		}
		if file.Mode()&os.ModeSymlink != 0 || file.FileInfo().Mode().IsRegular() == false {
			return fmt.Errorf("translation model archive contains a non-regular file %q", name)
		}
		if found[relative] {
			return fmt.Errorf("translation model archive repeats %q", name)
		}
		found[relative] = true
		expanded += int64(file.UncompressedSize64)
		if expanded > maxExpandedSize {
			return errors.New("translation model archive expands beyond the allowed size")
		}
		if err := extractArchiveFile(ctx, file, filepath.Join(destination, targetRelative)); err != nil {
			return err
		}
	}
	for required := range allowed {
		if !found[required] {
			return fmt.Errorf("translation model archive is missing %q", required)
		}
	}
	metadataData, err := os.ReadFile(filepath.Join(destination, "metadata.json"))
	if err != nil {
		return fmt.Errorf("read translation model metadata: %w", err)
	}
	var metadata modelMetadata
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		return fmt.Errorf("decode translation model metadata: %w", err)
	}
	if metadata.PackageVersion != ModelVersion || metadata.FromCode != "en" || metadata.ToCode != "pb" {
		return errors.New("downloaded translation model does not match English to Brazilian Portuguese")
	}
	if err := os.Remove(filepath.Join(destination, "metadata.json")); err != nil {
		return fmt.Errorf("remove temporary translation metadata: %w", err)
	}
	return nil
}

func extractArchiveFile(ctx context.Context, source *zip.File, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create translation model subdirectory: %w", err)
	}
	input, err := source.Open()
	if err != nil {
		return fmt.Errorf("open translation model file %q: %w", source.Name, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create translation model file %q: %w", source.Name, err)
	}
	_, copyErr := io.Copy(output, contextReader{ctx: ctx, reader: input})
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("extract translation model file %q: %w", source.Name, copyErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync translation model file %q: %w", source.Name, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close translation model file %q: %w", source.Name, closeErr)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open translation model directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync translation model directory: %w", err)
	}
	return nil
}
