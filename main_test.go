package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCrawl(t *testing.T) {
	var active, peak atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("external server was contacted") }))
	defer external.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for p := peak.Load(); n > p; p = peak.Load() {
			if peak.CompareAndSwap(p, n) {
				break
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/" {
			for i := 0; i < 12; i++ {
				fmt.Fprintf(w, `<a href="/p%d?q=1#fragment">link</a>`, i)
			}
			fmt.Fprint(w, `<a href="/p0?q=1">duplicate</a><a href="`+external.URL+`">outside</a>`)
			return
		}
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "<h1>Hello</h1><p>World</p>")
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := run(context.Background(), server.URL, dir, 4, 100); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 4 || peak.Load() < 2 {
		t.Fatalf("unexpected peak concurrency: %d", peak.Load())
	}
	count := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if !strings.Contains(string(b), "Source:") {
				t.Errorf("missing source in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 13 {
		t.Fatalf("got %d files, want 13", count)
	}
	u, _ := url.Parse(server.URL + "/p0?q=1")
	b, err := os.ReadFile(outputPath(dir, u))
	if err != nil || !strings.Contains(string(b), "# Hello") {
		t.Fatalf("Markdown missing: %s, %v", b, err)
	}
	u2, _ := url.Parse(server.URL + "/p0?q=2")
	if outputPath(dir, u) == outputPath(dir, u2) {
		t.Fatal("query URLs collide")
	}
}
func TestExternalRedirect(t *testing.T) {
	var contacted atomic.Bool
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Store(true) }))
	defer external.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, external.URL, http.StatusFound) }))
	defer server.Close()
	if err := run(context.Background(), server.URL, t.TempDir(), 1, 1); err == nil {
		t.Fatal("expected redirect error")
	}
	if contacted.Load() {
		t.Fatal("followed external redirect")
	}
}

func TestRequestHeaders(t *testing.T) {
	t.Setenv("CRAWL_COOKIE", "example=test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "example=test" || !strings.Contains(r.Header.Get("User-Agent"), "Chrome/156") || !strings.Contains(r.Header.Get("Accept"), "text/html") {
			t.Error("request headers were not sent")
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<p>ok</p>")
	}))
	defer server.Close()
	if err := run(context.Background(), server.URL, t.TempDir(), 1, 1); err != nil {
		t.Fatal(err)
	}
}

func TestBaseAndCharset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		if r.URL.Path == "/" {
			w.Write([]byte("<base href='/section/'><a href='page'>caf\xe9</a>"))
			return
		}
		if r.URL.Path != "/section/page" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte("<h1>caf\xe9</h1>"))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := run(context.Background(), server.URL, dir, 1, 10); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(server.URL + "/section/page")
	b, err := os.ReadFile(outputPath(dir, u))
	if err != nil || !strings.Contains(string(b), "café") {
		t.Fatalf("decoded Markdown: %q, %v", b, err)
	}
}
