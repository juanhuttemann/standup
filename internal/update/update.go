package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	defaultBaseURL = "https://github.com/juanhuttemann/standup"
	checksumsName  = "standup_checksums.txt"
	maxArchive     = 64 << 20
	maxChecksum    = 1 << 20
	maxBinary      = 32 << 20
)

type State int

const (
	UpToDate State = iota + 1
	UpgradeAvailable
	NewerInstalled
)

type Result struct {
	Current string
	Latest  string
	State   State
	Updated bool
	// LeftoverBackup is a backup of the previous executable that could not be
	// deleted yet. The update succeeded; the next run sweeps the file.
	LeftoverBackup string
}

type Platform struct {
	OS   string
	Arch string
}

type Client struct {
	HTTP             *http.Client
	BaseURL          string
	MaxArchiveBytes  int64
	MaxChecksumBytes int64
	MaxBinaryBytes   int64
}

func CompareVersions(current, latest string) (State, error) {
	cur, err := canonicalVersion(current)
	if err != nil {
		return 0, fmt.Errorf("current version: %w", err)
	}
	newest, err := canonicalVersion(latest)
	if err != nil {
		return 0, fmt.Errorf("latest version: %w", err)
	}
	switch semver.Compare(cur, newest) {
	case -1:
		return UpgradeAvailable, nil
	case 0:
		return UpToDate, nil
	default:
		return NewerInstalled, nil
	}
}

func canonicalVersion(v string) (string, error) {
	v = "v" + strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !semver.IsValid(v) {
		return "", fmt.Errorf("invalid semantic version %q", strings.TrimPrefix(v, "v"))
	}
	return v, nil
}

func AssetName(p Platform) (string, error) {
	if (p.OS != "linux" && p.OS != "darwin" && p.OS != "windows") ||
		(p.Arch != "amd64" && p.Arch != "arm64") {
		return "", fmt.Errorf("unsupported platform %s/%s", p.OS, p.Arch)
	}
	name := "standup_" + p.OS + "_" + p.Arch
	if p.OS == "windows" {
		return name + ".zip", nil
	}
	return name + ".tar.gz", nil
}

func Run(ctx context.Context, currentVersion string, checkOnly bool) (Result, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	c := Client{
		HTTP: httpClient, BaseURL: defaultBaseURL,
		MaxArchiveBytes: maxArchive, MaxChecksumBytes: maxChecksum, MaxBinaryBytes: maxBinary,
	}
	tag, err := c.Latest(ctx)
	if err != nil {
		return Result{}, err
	}
	state, err := CompareVersions(currentVersion, tag)
	if err != nil {
		return Result{}, err
	}
	result := Result{Current: normalizeDisplay(currentVersion), Latest: tag, State: state}
	if state != UpgradeAvailable || checkOnly {
		return result, nil
	}
	binary, err := c.Download(ctx, tag, Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		return Result{}, err
	}
	current, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("locate executable: %w", err)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return Result{}, fmt.Errorf("resolve executable: %w", err)
	}
	leftover, err := Install(current, binary, verifyVersion(tag))
	if err != nil {
		return Result{}, err
	}
	result.Updated = true
	result.LeftoverBackup = leftover
	return result, nil
}

func normalizeDisplay(v string) string {
	return "v" + strings.TrimPrefix(v, "v")
}

func (c Client) Latest(ctx context.Context) (tag string, err error) {
	client := c.httpClient()
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(c.baseURL(), "/")+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := copyClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || location == "" {
		return "", fmt.Errorf("resolve latest release: expected redirect, got %s", resp.Status)
	}
	u, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("resolve latest release redirect: %w", err)
	}
	tag, err = canonicalVersion(filepath.Base(u.Path))
	if err != nil {
		return "", err
	}
	return tag, nil
}

func (c Client) Download(ctx context.Context, tag string, p Platform) ([]byte, error) {
	tag, err := canonicalVersion(tag)
	if err != nil {
		return nil, err
	}
	asset, err := AssetName(p)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(c.baseURL(), "/") + "/releases/download/" + tag + "/"
	checksumBody, err := c.get(ctx, base+checksumsName, c.checksumLimit())
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, err := parseChecksum(bytes.NewReader(checksumBody), asset)
	if err != nil {
		return nil, err
	}
	archive, err := c.get(ctx, base+asset, c.archiveLimit())
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	if got := sha256.Sum256(archive); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s", asset)
	}
	return extractArchive(bytes.NewReader(archive), asset, c.binaryLimit())
}

func (c Client) get(ctx context.Context, rawURL string, limit int64) (body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return readLimited(resp.Body, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return b, nil
}

func parseChecksum(r io.Reader, asset string) ([sha256.Size]byte, error) {
	var found [sha256.Size]byte
	matches := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return found, fmt.Errorf("invalid checksum for %s", asset)
		}
		copy(found[:], digest)
		matches++
	}
	if err := scanner.Err(); err != nil {
		return found, err
	}
	if matches != 1 {
		return found, fmt.Errorf("expected exactly one checksum for %s, got %d", asset, matches)
	}
	return found, nil
}

func extractArchive(r io.Reader, asset string, limit int64) (binary []byte, err error) {
	if strings.HasSuffix(asset, ".zip") {
		archive, err := io.ReadAll(r)
		if err != nil {
			return nil, err
		}
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("open archive: %w", err)
		}
		if len(zr.File) != 1 || zr.File[0].Name != "standup.exe" || !zr.File[0].Mode().IsRegular() {
			return nil, errors.New("archive must contain only the standup.exe binary")
		}
		file, err := zr.File[0].Open()
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		return readLimited(file, limit)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	tr := tar.NewReader(gz)
	header, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if header.Name != "standup" || header.Typeflag != tar.TypeReg || header.Size > limit {
		return nil, errors.New("archive must contain only the standup binary")
	}
	binary, err = readLimited(tr, limit)
	if err != nil {
		return nil, err
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return nil, errors.New("archive contains unexpected entries")
	}
	return binary, nil
}

// Install replaces the running executable and returns any backup left behind.
// Only Windows makes one: it locks a running executable, so the updating
// process cannot delete its own backup. The glob finds nothing anywhere else.
func Install(current string, binary []byte, verify func(string) error) (leftover string, err error) {
	sweepBackups(current)
	info, err := os.Stat(current)
	if err != nil {
		return "", fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("executable is not a regular file: %s", current)
	}
	pattern := ".standup-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := os.CreateTemp(filepath.Dir(current), pattern)
	if err != nil {
		return "", fmt.Errorf("create update beside executable: %w", err)
	}
	candidate := f.Name()
	defer func() {
		if removeErr := os.Remove(candidate); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err := f.Chmod(info.Mode().Perm()); err != nil {
		return "", errors.Join(err, f.Close())
	}
	if _, err := f.Write(binary); err != nil {
		return "", errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return "", errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := verify(candidate); err != nil {
		return "", fmt.Errorf("verify downloaded binary: %w", err)
	}
	if err := replaceExecutable(current, candidate); err != nil {
		return "", err
	}
	return firstBackup(current), syncDir(filepath.Dir(current))
}

// backupGlob matches the backups replaceExecutable leaves on Windows. Naming
// them by pid is what lets a later run tell its own backup from a live one.
func backupGlob(current string) []string {
	matches, err := filepath.Glob(current + ".old-*")
	if err != nil {
		return nil
	}
	return matches
}

// sweepBackups deletes what earlier updates could not. The process that made
// a backup is the one process that cannot delete it; by the next run it is
// gone. Anything still locked is simply tried again next time.
func sweepBackups(current string) {
	for _, path := range backupGlob(current) {
		_ = os.Remove(path)
	}
}

func firstBackup(current string) string {
	if matches := backupGlob(current); len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func verifyVersion(want string) func(string) error {
	return func(path string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, path, "--version").Output()
		if err != nil {
			return err
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 || normalizeDisplay(fields[len(fields)-1]) != want {
			return fmt.Errorf("expected %s, got %q", want, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c Client) archiveLimit() int64 {
	if c.MaxArchiveBytes > 0 {
		return c.MaxArchiveBytes
	}
	return maxArchive
}
func (c Client) checksumLimit() int64 {
	if c.MaxChecksumBytes > 0 {
		return c.MaxChecksumBytes
	}
	return maxChecksum
}
func (c Client) binaryLimit() int64 {
	if c.MaxBinaryBytes > 0 {
		return c.MaxBinaryBytes
	}
	return maxBinary
}
