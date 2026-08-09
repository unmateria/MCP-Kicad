package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// userAgent identifies this client. Several of the sources refuse a request
// with Go's default agent — EasyEDA answers 403 to anything that does not look
// like a browser — so every request in this package goes through one place
// that sets it.
const userAgent = "mcp-kicad/0.2 (+https://github.com/unmateria/MCP-Kicad)"

// maxAssetBytes caps a single download. The largest legitimate file here is a
// symbol library of about 1.2 MB; anything past 32 MB is a mirror serving an
// HTML error page or a repository we misread, and reading it into memory to
// find out is how a search turns into an out-of-memory.
const maxAssetBytes = 32 << 20

// get fetches a URL and returns its body.
func get(ctx context.Context, client *http.Client, url string, header map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	if len(data) > maxAssetBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxAssetBytes)
	}
	return data, nil
}

// getEach fetches several URLs with bounded concurrency, calling fn for each
// success in an unspecified order. Failures are collected, not fatal: one
// missing library must not abort an index build over thirty of them.
//
// fn is called under a lock, so it does not need to be safe for concurrent use.
func getEach(ctx context.Context, client *http.Client, urls []string, workers int, fn func(url string, data []byte)) []error {
	if workers < 1 {
		workers = 1
	}
	var (
		mu   sync.Mutex
		errs []error
	)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, u := range urls {
		select {
		case <-ctx.Done():
			mu.Lock()
			errs = append(errs, ctx.Err())
			mu.Unlock()
			wg.Wait()
			return errs
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := get(ctx, client, u, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			fn(u, data)
		}(u)
	}
	wg.Wait()
	return errs
}
