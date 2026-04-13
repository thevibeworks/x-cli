package api

// Media download. We don't upload — XActions' chunked uploader is out of
// scope for v0.1. We extract media URLs from a parsed Tweet and stream
// each one to disk with progress reporting.
//
// Image quality: Twitter's `media_url_https` returns the original-quality
// image when you append `?name=large`. For most photos `large` is enough;
// `orig` returns the absolute original which is sometimes huge.
//
// Video: ParseTweet already picked the highest-bitrate `video/mp4`
// variant, so we just GET it.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MediaDownload describes one downloaded asset.
type MediaDownload struct {
	URL    string
	Path   string
	Bytes  int64
	Type   string // photo | video | animated_gif
}

// DownloadOptions tunes a media download batch.
type DownloadOptions struct {
	OutDir string
	// Quality is the X CDN size hint applied to image URLs:
	//   "large" (default), "medium", "small", "orig", "" (no hint)
	Quality string
	// OnProgress is called once per file with the asset just written.
	OnProgress func(d MediaDownload)
}

// DownloadTweetMedia downloads every media asset attached to a tweet to
// `opts.OutDir`. Returns one MediaDownload per saved file. Skips an asset
// (without erroring) when its URL is empty.
//
// Filenames are `<tweetID>_<index>.<ext>`, with .mp4 for videos/GIFs and
// the source extension for photos.
func (c *Client) DownloadTweetMedia(ctx context.Context, t *Tweet, opts DownloadOptions) ([]MediaDownload, error) {
	if t == nil {
		return nil, fmt.Errorf("DownloadTweetMedia: nil tweet")
	}
	if opts.OutDir == "" {
		opts.OutDir = "."
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}

	out := make([]MediaDownload, 0, len(t.Media))
	for i, m := range t.Media {
		// Pick best URL: video first if present, otherwise the photo URL.
		var src string
		var ext string
		switch m.Type {
		case "video", "animated_gif":
			src = m.VideoURL
			if src == "" {
				src = m.URL
			}
			ext = ".mp4"
		default:
			src = applyImageQuality(m.URL, opts.Quality)
			ext = extFromURL(m.URL)
			if ext == "" {
				ext = ".jpg"
			}
		}
		if src == "" {
			continue
		}

		dest := filepath.Join(opts.OutDir, fmt.Sprintf("%s_%d%s", t.ID, i, ext))
		n, err := c.downloadFile(ctx, src, dest)
		if err != nil {
			return out, fmt.Errorf("download %s: %w", src, err)
		}
		d := MediaDownload{URL: src, Path: dest, Bytes: n, Type: m.Type}
		if opts.OnProgress != nil {
			opts.OnProgress(d)
		}
		out = append(out, d)
	}
	return out, nil
}

// downloadFile streams a URL to disk through the same http.Client the
// Client uses, so any future TLS impersonation round-tripper applies.
// Writes the body atomically via temp+rename.
func (c *Client) downloadFile(ctx context.Context, src, dest string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("http %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// applyImageQuality appends `?name=<size>` to an X image URL. Replaces
// any existing `name=` parameter so we don't double-stack hints.
func applyImageQuality(rawURL, quality string) string {
	if rawURL == "" || quality == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("name", quality)
	u.RawQuery = q.Encode()
	return u.String()
}

// extFromURL returns the lowercased extension of a URL path, including
// the leading dot. Returns "" if there isn't one.
func extFromURL(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(parsed.Path))
}
