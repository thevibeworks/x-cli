package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadTweetMediaImageAndVideo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Distinguish image vs video by URL
		switch {
		case strings.Contains(r.URL.Path, "img"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("FAKE-JPEG-BYTES"))
		case strings.Contains(r.URL.Path, "vid"):
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("FAKE-MP4-BYTES-LONGER-THAN-IMAGE"))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New(Options{
		Endpoints: &EndpointMap{Bases: Bases{GraphQL: srv.URL, REST: srv.URL}, Bearer: "B"},
		Throttle:  NewThrottle(Defaults{}),
	})

	tweet := &Tweet{
		ID: "tw1",
		Media: []TweetMedia{
			{Type: "photo", URL: srv.URL + "/img/photo.jpg"},
			{Type: "video", URL: srv.URL + "/img/poster.jpg", VideoURL: srv.URL + "/vid/clip.mp4"},
		},
	}

	dir := t.TempDir()
	results, err := c.DownloadTweetMedia(context.Background(), tweet, DownloadOptions{
		OutDir:  dir,
		Quality: "large",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d files, want 2", len(results))
	}
	for _, r := range results {
		fi, err := os.Stat(r.Path)
		if err != nil {
			t.Errorf("missing file %q: %v", r.Path, err)
			continue
		}
		if fi.Size() == 0 {
			t.Errorf("empty file %q", r.Path)
		}
	}
	// Confirm the video file got the .mp4 extension.
	if filepath.Ext(results[1].Path) != ".mp4" {
		t.Errorf("video file ext = %q", filepath.Ext(results[1].Path))
	}
}

func TestDownloadTweetMediaSkipsEmptyURL(t *testing.T) {
	c := New(Options{
		Endpoints: &EndpointMap{Bases: Bases{GraphQL: "http://x", REST: "http://x"}, Bearer: "B"},
		Throttle:  NewThrottle(Defaults{}),
	})

	tweet := &Tweet{
		ID:    "tw2",
		Media: []TweetMedia{{Type: "photo", URL: ""}},
	}
	dir := t.TempDir()
	results, err := c.DownloadTweetMedia(context.Background(), tweet, DownloadOptions{OutDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 files for empty URL, got %d", len(results))
	}
}

func TestApplyImageQuality(t *testing.T) {
	cases := map[string]string{
		"https://x.com/img.jpg":              "https://x.com/img.jpg?name=large",
		"https://x.com/img.jpg?name=small":   "https://x.com/img.jpg?name=large",
		"":                                    "",
		"https://x.com/img.jpg?foo=bar":      "https://x.com/img.jpg?foo=bar&name=large",
	}
	for in, want := range cases {
		if got := applyImageQuality(in, "large"); got != want {
			t.Errorf("applyImageQuality(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtFromURL(t *testing.T) {
	cases := map[string]string{
		"https://cdn.x.com/img.jpg?name=lg":  ".jpg",
		"https://cdn.x.com/clip.mp4":         ".mp4",
		"https://cdn.x.com/anim.gif":         ".gif",
		"https://x.com/jack/status/12345":    "", // no extension in path
		"":                                    "",
	}
	for in, want := range cases {
		if got := extFromURL(in); got != want {
			t.Errorf("extFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
