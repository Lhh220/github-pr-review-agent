package githubtools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultStaticCheckTimeout = 2 * time.Minute
	maxStaticCheckFiles       = 2000
	maxStaticArchiveBytes     = 128 << 20
	maxStaticFileBytes        = 2 << 20
	maxStaticCheckOutputChars = 16 << 10
)

var staticCheckCommands = map[string][]string{
	"go_test": {"test", "./..."},
	"go_vet":  {"vet", "./..."},
}

type staticCheckRunner func(
	ctx context.Context,
	args []string,
	dir string,
	env []string,
) (staticCheckCommandResult, error)

type staticCheckCommandResult struct {
	Name            string `json:"name"`
	Command         string `json:"command"`
	Success         bool   `json:"success"`
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	DurationMS      int64  `json:"duration_ms"`
	Output          string `json:"output"`
	OutputTruncated bool   `json:"output_truncated"`
	Error           string `json:"error,omitempty"`
}

type staticChecksTool struct {
	toolkit *Toolkit
}

func (staticChecksTool) Name() string { return "run_static_checks" }

func (staticChecksTool) Description() string {
	return "Run server-defined Go static checks for the pull request head. Only go_test and go_vet are allowed; commands and arguments are fixed by the server."
}

func (staticChecksTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"checks": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string", "enum": []string{"go_test", "go_vet"}},
				"description": "Checks to run. Omit to run both go_test and go_vet.",
			},
		},
		"additionalProperties": false,
	}
}

func (t staticChecksTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	checks, err := staticCheckNames(input)
	if err != nil {
		return "", err
	}

	pr, err := t.toolkit.pullRequest(ctx)
	if err != nil {
		return "", err
	}
	baseDir := t.toolkit.staticCheckWorkDir
	if baseDir == "" {
		return "", fmt.Errorf("static check work directory is not configured")
	}
	baseDir, err = filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve static check work directory: %w", err)
	}
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", fmt.Errorf("create static check work directory: %w", err)
	}
	for _, name := range []string{"home", "gocache", "gomodcache", "gopath", "gotmp"} {
		if err := os.MkdirAll(filepath.Join(baseDir, name), 0o700); err != nil {
			return "", fmt.Errorf("create static check %s directory: %w", name, err)
		}
	}
	repoDir, err := os.MkdirTemp(baseDir, "repo-")
	if err != nil {
		return "", fmt.Errorf("create static check repository directory: %w", err)
	}
	defer os.RemoveAll(repoDir)

	archive, err := t.toolkit.client.GetRepositoryTarball(
		ctx,
		t.toolkit.owner,
		t.toolkit.repo,
		pr.Head.SHA,
	)
	if err != nil {
		return "", fmt.Errorf("get repository tarball: %w", err)
	}
	defer archive.Close()
	if err := extractStaticCheckArchive(archive, repoDir); err != nil {
		return "", fmt.Errorf("extract repository archive: %w", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return encodeJSON(map[string]any{
				"supported": false,
				"reason":    "go.mod not found at repository root",
				"ref":       pr.Head.SHA,
			})
		}
		return "", fmt.Errorf("stat go.mod: %w", err)
	}

	env := staticCheckEnvironment(baseDir, t.toolkit.staticCheckGoProxy)

	results := make([]staticCheckCommandResult, 0, len(checks))
	for _, check := range checks {
		args := staticCheckCommands[check]
		started := time.Now()
		runCtx, cancel := context.WithTimeout(ctx, t.toolkit.staticCheckTimeout)
		result, runErr := t.toolkit.staticCheckRunner(runCtx, args, repoDir, env)
		cancel()
		result.Name = check
		result.Command = "go " + strings.Join(args, " ")
		result.DurationMS = time.Since(started).Milliseconds()
		if runErr != nil {
			result.Error = runErr.Error()
		}
		result.Output, result.OutputTruncated = clampString(result.Output, maxStaticCheckOutputChars)
		results = append(results, result)
	}

	return encodeJSON(map[string]any{
		"supported": true,
		"ref":       pr.Head.SHA,
		"checks":    results,
		"timeout":   t.toolkit.staticCheckTimeout.String(),
	})
}

func staticCheckNames(input map[string]any) ([]string, error) {
	rawChecks, exists := input["checks"]
	if !exists || rawChecks == nil {
		return []string{"go_test", "go_vet"}, nil
	}

	values, ok := rawChecks.([]any)
	if !ok {
		return nil, fmt.Errorf("checks must be an array")
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("checks must not be empty")
	}

	checks := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		check, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("each check must be a string")
		}
		if _, allowed := staticCheckCommands[check]; !allowed {
			return nil, fmt.Errorf("unsupported check %q", check)
		}
		if seen[check] {
			continue
		}
		seen[check] = true
		checks = append(checks, check)
	}
	return checks, nil
}

func extractStaticCheckArchive(archive io.Reader, destination string) error {
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()

	var (
		fileCount    int
		archiveBytes int64
		tarReader    = tar.NewReader(gzipReader)
	)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}

		relativePath := normalizeArchivePath(header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			targetDir := destination
			if relativePath != "" {
				targetDir = filepath.Join(destination, filepath.FromSlash(relativePath))
			}
			if err := os.MkdirAll(targetDir, 0o700); err != nil {
				return fmt.Errorf("create archive directory: %w", err)
			}
			continue
		case tar.TypeReg:
			if relativePath == "" {
				return fmt.Errorf("unsafe archive path %q", header.Name)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive link entries are not allowed: %q", header.Name)
		default:
			continue
		}

		if header.Size > maxStaticFileBytes {
			return fmt.Errorf("archive file %q exceeds size limit", relativePath)
		}
		archiveBytes += header.Size
		if archiveBytes > maxStaticArchiveBytes {
			return fmt.Errorf("archive exceeds decompressed size limit")
		}
		fileCount++
		if fileCount > maxStaticCheckFiles {
			return fmt.Errorf("archive exceeds file count limit")
		}

		targetPath := filepath.Join(destination, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
			return fmt.Errorf("create file directory: %w", err)
		}
		file, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, staticFileMode(header.Mode))
		if err != nil {
			return fmt.Errorf("create archive file: %w", err)
		}
		written, copyErr := io.Copy(file, tarReader)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("write archive file: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close archive file: %w", closeErr)
		}
		if written != header.Size {
			return fmt.Errorf("archive file %q has unexpected size", relativePath)
		}
	}
}

func staticFileMode(mode int64) os.FileMode {
	permissions := os.FileMode(mode) & 0o777
	if permissions == 0 {
		return 0o400
	}
	return permissions
}

func staticCheckEnvironment(baseDir, goProxy string) []string {
	return []string{
		"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + filepath.Join(baseDir, "home"),
		"GOCACHE=" + filepath.Join(baseDir, "gocache"),
		"GOMODCACHE=" + filepath.Join(baseDir, "gomodcache"),
		"GOPATH=" + filepath.Join(baseDir, "gopath"),
		"GOTMPDIR=" + filepath.Join(baseDir, "gotmp"),
		"GOTOOLCHAIN=local",
		"GOENV=off",
		"GOPROXY=" + goProxy,
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=1",
	}
}

func runStaticCheckCommand(
	ctx context.Context,
	args []string,
	dir string,
	env []string,
) (staticCheckCommandResult, error) {
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	command.Env = env

	output, err := command.CombinedOutput()
	result := staticCheckCommandResult{
		Success: err == nil,
		Output:  string(output),
	}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled)
		result.Output = strings.TrimSpace(result.Output + "\n" + ctx.Err().Error())
		return result, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("start go command: %w", err)
}
