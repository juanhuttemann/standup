package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		name, current, latest string
		want                  State
		wantErr               bool
	}{
		{"equal", "0.8.1", "v0.8.1", UpToDate, false},
		{"upgrade", "v0.8.1", "v0.9.0", UpgradeAvailable, false},
		{"no downgrade", "0.10.0", "v0.9.0", NewerInstalled, false},
		{"build metadata equal", "0.8.1+local", "v0.8.1", UpToDate, false},
		{"development build", "dev", "v0.9.0", 0, true},
		{"bad latest", "0.8.1", "latest", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompareVersions(tc.current, tc.latest)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAssetName(t *testing.T) {
	got, err := AssetName(Platform{OS: "linux", Arch: "arm64"})
	require.NoError(t, err)
	assert.Equal(t, "standup_linux_arm64.tar.gz", got)
	got, err = AssetName(Platform{OS: "windows", Arch: "amd64"})
	require.NoError(t, err)
	assert.Equal(t, "standup_windows_amd64.zip", got)
	_, err = AssetName(Platform{OS: "freebsd", Arch: "amd64"})
	assert.Error(t, err)
}

func TestParseChecksumRequiresOneExactValidEntry(t *testing.T) {
	digest := sha256.Sum256([]byte("archive"))
	got, err := parseChecksum(bytes.NewBufferString(fmt.Sprintf("%x  standup_linux_amd64.tar.gz\n", digest)), "standup_linux_amd64.tar.gz")
	require.NoError(t, err)
	assert.Equal(t, digest, got)

	for name, body := range map[string]string{
		"missing":   fmt.Sprintf("%x  other.tar.gz\n", digest),
		"duplicate": fmt.Sprintf("%x  standup_linux_amd64.tar.gz\n%x  standup_linux_amd64.tar.gz\n", digest, digest),
		"malformed": "not-a-digest  standup_linux_amd64.tar.gz\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseChecksum(bytes.NewBufferString(body), "standup_linux_amd64.tar.gz")
			assert.Error(t, err)
		})
	}
}

func TestExtractArchiveRejectsUnsafeEntries(t *testing.T) {
	valid := tarGz(t, map[string][]byte{"standup": []byte("binary")})
	got, err := extractArchive(bytes.NewReader(valid), "standup_linux_amd64.tar.gz", 1024)
	require.NoError(t, err)
	assert.Equal(t, []byte("binary"), got)

	for name, archive := range map[string][]byte{
		"nested": tarGz(t, map[string][]byte{"bin/standup": []byte("binary")}),
		"extra":  tarGz(t, map[string][]byte{"standup": []byte("binary"), "README": []byte("x")}),
		"large":  tarGz(t, map[string][]byte{"standup": bytes.Repeat([]byte("x"), 1025)}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extractArchive(bytes.NewReader(archive), "standup_linux_amd64.tar.gz", 1024)
			assert.Error(t, err)
		})
	}
}

func TestClientDownloadsPinnedVerifiedCandidate(t *testing.T) {
	archive := tarGz(t, map[string][]byte{"standup": []byte("binary")})
	digest := sha256.Sum256(archive)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch filepath.Base(r.URL.Path) {
		case checksumsName:
			_, err := fmt.Fprintf(w, "%x  standup_linux_amd64.tar.gz\n", digest)
			require.NoError(t, err)
		default:
			_, err := w.Write(archive)
			require.NoError(t, err)
		}
	}))
	t.Cleanup(srv.Close)

	c := Client{HTTP: srv.Client(), BaseURL: srv.URL, MaxArchiveBytes: 4096, MaxChecksumBytes: 1024, MaxBinaryBytes: 1024}
	got, err := c.Download(context.Background(), "v0.9.0", Platform{OS: "linux", Arch: "amd64"})
	require.NoError(t, err)
	assert.Equal(t, []byte("binary"), got)
	assert.Equal(t, []string{
		"/releases/download/v0.9.0/standup_checksums.txt",
		"/releases/download/v0.9.0/standup_linux_amd64.tar.gz",
	}, paths)
}

func TestClientLatestReadsValidatedRedirectTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/releases/tag/v0.9.0")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL}
	tag, err := c.Latest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v0.9.0", tag)
}

func TestExtractWindowsArchive(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("standup.exe")
	require.NoError(t, err)
	_, err = f.Write([]byte("binary"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	got, err := extractArchive(bytes.NewReader(archive.Bytes()), "standup_windows_amd64.zip", 1024)
	require.NoError(t, err)
	assert.Equal(t, []byte("binary"), got)
}

func TestClientFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body []byte
	}{
		{"http error", http.StatusNotFound, nil},
		{"oversized checksum", http.StatusOK, bytes.Repeat([]byte("x"), 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, err := w.Write(tc.body)
				require.NoError(t, err)
			}))
			t.Cleanup(srv.Close)
			c := Client{HTTP: srv.Client(), BaseURL: srv.URL, MaxArchiveBytes: 4096, MaxChecksumBytes: 1024, MaxBinaryBytes: 1024}
			_, err := c.Download(context.Background(), "v0.9.0", Platform{OS: "linux", Arch: "amd64"})
			assert.Error(t, err)
		})
	}
}

func TestInstallValidatesBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "standup")
	require.NoError(t, os.WriteFile(current, []byte("old"), 0o755))
	_, err := Install(current, []byte("new"), func(path string) error {
		b, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		assert.Equal(t, []byte("new"), b)
		return assert.AnError
	})
	assert.Error(t, err)
	b, readErr := os.ReadFile(current)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("old"), b)
}

func TestInstallReplacesValidatedBinaryAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "standup")
	require.NoError(t, os.WriteFile(current, []byte("old"), 0o751))
	leftover, err := Install(current, []byte("new"), func(string) error { return nil })
	require.NoError(t, err)
	assert.Empty(t, leftover, "no platform but Windows leaves a backup behind")
	b, err := os.ReadFile(current)
	require.NoError(t, err)
	assert.Equal(t, []byte("new"), b)
	info, err := os.Stat(current)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		// Windows has no Unix permission bits; every file reads as 0666.
		assert.Equal(t, os.FileMode(0o751), info.Mode().Perm())
	}
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := io.Copy(tw, bytes.NewReader(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return out.Bytes()
}
