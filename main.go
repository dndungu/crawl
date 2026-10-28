// Command crawl downloads pages from one origin and saves them as Markdown.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const maxBody = 10 << 20

type result struct {
	url   string
	links []string
	err   error
}
type crawler struct {
	origin *url.URL
	output string
	client *http.Client
}

func normalize(u *url.URL) (string, error) {
	v := *u
	v.Fragment = ""
	v.Scheme = strings.ToLower(v.Scheme)
	v.Host = strings.ToLower(v.Host)
	if (v.Scheme != "http" && v.Scheme != "https") || v.Hostname() == "" || v.User != nil {
		return "", errors.New("URL must be an HTTP(S) URL without credentials")
	}
	if v.Path == "" {
		v.Path = "/"
	}
	return v.String(), nil
}
func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// A hash of the complete URL prevents collisions between paths and query strings.
func outputPath(root string, u *url.URL) string {
	sum := sha256.Sum256([]byte(u.String()))
	host := strings.ReplaceAll(u.Host, ":", "_")
	// Escaped path components cannot escape the output directory.
	parts := []string{root, host}
	for _, p := range strings.Split(strings.Trim(u.EscapedPath(), "/"), "/") {
		if p != "" && p != "." && p != ".." {
			parts = append(parts, p)
		}
	}
	parts = append(parts, "page-"+hex.EncodeToString(sum[:8])+".md")
	return filepath.Join(parts...)
}
func (c *crawler) fetch(ctx context.Context, raw string) result {
	out := result{url: raw}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		out.err = err
		return out
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/jxl,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Priority", "u=0, i")
	req.Header.Set("Sec-Ch-Ua", `"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36")
	if cookie := os.Getenv("CRAWL_COOKIE"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		out.err = err
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.err = fmt.Errorf("HTTP %s", resp.Status)
		return out
	}
	contentType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (contentType != "text/html" && contentType != "application/xhtml+xml") {
		out.err = fmt.Errorf("unsupported content type %q", resp.Header.Get("Content-Type"))
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		out.err = err
		return out
	}
	if len(body) > maxBody {
		out.err = errors.New("page exceeds 10 MiB limit")
		return out
	}
	decoded, err := charset.NewReader(bytes.NewReader(body), resp.Header.Get("Content-Type"))
	if err != nil {
		out.err = err
		return out
	}
	doc, err := html.Parse(decoded)
	if err != nil {
		out.err = err
		return out
	}
	base := resp.Request.URL
	var findBase func(*html.Node) bool
	findBase = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "base" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					if ref, e := url.Parse(a.Val); e == nil {
						base = base.ResolveReference(ref)
					}
					return true
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if findBase(child) {
				return true
			}
		}
		return false
	}
	findBase(doc)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					if ref, e := url.Parse(a.Val); e == nil {
						u := base.ResolveReference(ref)
						if sameOrigin(c.origin, u) {
							if s, e := normalize(u); e == nil {
								out.links = append(out.links, s)
							}
						}
					}
					break
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	markdown, err := md.ConvertNode(doc)
	if err != nil {
		out.err = err
		return out
	}
	finalURL, err := normalize(resp.Request.URL)
	if err != nil {
		out.err = err
		return out
	}
	final, _ := url.Parse(finalURL)
	dest := outputPath(c.output, final)
	if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		out.err = err
		return out
	}
	// Write atomically so interruptions do not leave partial pages.
	f, err := os.CreateTemp(filepath.Dir(dest), ".crawl-*.tmp")
	if err != nil {
		out.err = err
		return out
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = fmt.Fprintf(f, "<!-- Source: %s -->\n\n%s\n", strings.ReplaceAll(finalURL, "--", "%2D%2D"), markdown)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		out.err = err
		return out
	}
	log.Printf("saved %s -> %s", finalURL, dest)
	return out
}
func run(ctx context.Context, start, output string, concurrency, maxPages int) error {
	raw, err := url.Parse(start)
	if err != nil {
		return err
	}
	start, err = normalize(raw)
	if err != nil {
		return err
	}
	origin, _ := url.Parse(start)
	if concurrency < 1 || concurrency > 16 {
		return errors.New("concurrency must be between 1 and 16")
	}
	if maxPages < 1 {
		return errors.New("max-pages must be positive")
	}
	c := &crawler{origin: origin, output: output}
	c.client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !sameOrigin(origin, req.URL) {
			return errors.New("redirect outside starting origin")
		}
		return nil
	}}
	jobs := make(chan string)
	results := make(chan result, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			for u := range jobs {
				results <- c.fetch(ctx, u)
			}
		}()
	}
	defer close(jobs)
	queue := []string{start}
	seen := map[string]bool{start: true}
	active, failures := 0, 0
	for len(queue) > 0 || active > 0 {
		var send chan string
		var next string
		if len(queue) > 0 && active < concurrency {
			send = jobs
			next = queue[0]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case send <- next:
			queue = queue[1:]
			active++
		case r := <-results:
			active--
			if r.err != nil {
				failures++
				log.Printf("failed %s: %v", r.url, r.err)
				continue
			}
			for _, link := range r.links {
				if !seen[link] && len(seen) < maxPages {
					seen[link] = true
					queue = append(queue, link)
				}
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d page(s) failed", failures)
	}
	return nil
}
func main() {
	output := flag.String("output", "crawl-output", "directory for Markdown files")
	concurrency := flag.Int("concurrency", 16, "maximum concurrent requests (1-16)")
	maxPages := flag.Int("max-pages", 1000, "maximum number of URLs to crawl")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] START_URL\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, flag.Arg(0), *output, *concurrency, *maxPages); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
