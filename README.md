# crawl

A small Go command-line crawler that saves HTML pages from a single origin as Markdown.

```sh
go run . -output ./pages -concurrency 16 -max-pages 1000 https://example.com/
```

The start URL and flags must be supplied in the order shown. Links are resolved relative to the fetched page (or its HTML base URL) and followed only when their scheme, host, and effective port match the start URL. Fragments are discarded; query strings are retained. At most 16 requests run concurrently. The default limit is 1,000 discovered URLs, including the starting URL; use `-max-pages` to change it. Redirects outside the origin are rejected. Different discovered URLs that redirect to the same page each count toward the limit and may write the same output file.

Requests use browser-style headers. To send a cookie, set `CRAWL_COOKIE` in the environment to the full Cookie header value before starting the crawler. It is sent to pages on the starting origin, so use a cookie scoped to that site and keep it out of shell history and shared logs. Cookies may expire or be bound to a browser session.

Files are organized under `output/host/path/page-<URL hash>.md`; the hash distinguishes query strings and other URL variants, and a source URL comment identifies each page. Existing files for the same URL are replaced. Non-HTML responses, HTTP errors, pages over 10 MiB, and failed requests are reported; the process exits nonzero if any page fails. Requests time out after 30 seconds. Interrupt with Ctrl-C to stop.

This crawler processes server-returned HTML; it does not execute JavaScript, interpret robots.txt, or enforce a request rate. Use it only where you have permission to crawl and choose limits appropriate for the site.
