package githubtools

import (
	"context"
	"fmt"
	"io"
	"os"
)

// cachedTarball downloads the repository tarball for ref once and stores it in
// a local temp file, so repeated tool calls during one review do not re-download
// a potentially large archive. Failures are not memoized so a later call can retry.
func (t *Toolkit) cachedTarball(ctx context.Context, ref string) (*os.File, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.tarballFile != nil {
		if t.tarballRef == ref {
			file, err := os.Open(t.tarballPath)
			if err == nil {
				return file, nil
			}
		}
		t.removeTarballLocked()
	}

	archive, err := t.client.GetRepositoryTarball(ctx, t.owner, t.repo, ref)
	if err != nil {
		return nil, fmt.Errorf("get repository tarball: %w", err)
	}
	defer archive.Close()

	file, err := os.CreateTemp("", "pr-review-tarball-*.tgz")
	if err != nil {
		return nil, fmt.Errorf("create tarball cache file: %w", err)
	}
	written, copyErr := io.Copy(file, archive)
	closeErr := file.Close()
	if copyErr != nil {
		os.Remove(file.Name())
		return nil, fmt.Errorf("download repository tarball: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(file.Name())
		return nil, fmt.Errorf("close tarball cache file: %w", closeErr)
	}
	if written == 0 {
		os.Remove(file.Name())
		return nil, fmt.Errorf("repository tarball is empty")
	}

	t.tarballFile = file
	t.tarballPath = file.Name()
	t.tarballRef = ref

	cached, err := os.Open(t.tarballPath)
	if err != nil {
		t.removeTarballLocked()
		return nil, fmt.Errorf("open cached tarball: %w", err)
	}
	return cached, nil
}

func (t *Toolkit) removeTarballLocked() {
	if t.tarballPath != "" {
		os.Remove(t.tarballPath)
	}
	t.tarballFile = nil
	t.tarballPath = ""
	t.tarballRef = ""
}

// Close releases toolkit resources, including the cached tarball file.
// It is safe to call multiple times.
func (t *Toolkit) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.removeTarballLocked()
}
