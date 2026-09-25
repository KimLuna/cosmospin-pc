package cosmo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
)

// mediaHTTP has no total timeout: c.hc's 30s cap suits JSON API calls, but a
// video download routinely runs longer and would be cut off mid-transfer
// (http.Client.Timeout bounds the whole exchange, not idle time). Cancellation
// still works through the request context.
var mediaHTTP = &http.Client{}

// Basename returns the filename portion of a media URL (used to name and
// dedupe downloads).
func Basename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return path.Base(rawURL)
	}
	return path.Base(u.Path)
}

// Download streams a media URL to destPath, writing to a .part file first and
// renaming into place on success so an interrupted transfer can't be mistaken
// for a complete file. Media lives on a CDN and needs no auth header.
func (c *Client) Download(ctx context.Context, rawURL, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := mediaHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Body: "download failed"}
	}

	partPath := destPath + ".part"
	f, err := os.Create(partPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(partPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(partPath)
		return err
	}
	return os.Rename(partPath, destPath)
}

// DownloadPost saves every media file on a post into dir, skipping files whose
// basename already exists. Returns the count downloaded and skipped.
func (c *Client) DownloadPost(ctx context.Context, p Post, dir string) (downloaded, skipped int, err error) {
	for _, m := range p.Media {
		if m.URL == "" {
			continue
		}
		dest := filepath.Join(dir, Basename(m.URL))
		if _, statErr := os.Stat(dest); statErr == nil {
			skipped++
			continue
		}
		if err = c.Download(ctx, m.URL, dest); err != nil {
			return downloaded, skipped, fmt.Errorf("downloading %s: %w", Basename(m.URL), err)
		}
		downloaded++
	}
	return downloaded, skipped, nil
}
