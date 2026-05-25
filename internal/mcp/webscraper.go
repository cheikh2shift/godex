package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"golang.org/x/net/html"
)

type cacheEntry struct {
	html      string
	expiresAt time.Time
}

type WebScraperServer struct {
	allowedURLs   []string
	tools         []Tool
	browserCancel context.CancelFunc
	autoConfirm   bool
	cache         map[string]cacheEntry
	cacheMu       sync.Mutex
}

func NewWebScraperServer(allowedURLs []string, autoConfirm bool) *WebScraperServer {
	if len(allowedURLs) == 0 {
		allowedURLs = []string{}
	}

	return &WebScraperServer{
		allowedURLs: allowedURLs,
		autoConfirm: autoConfirm,
		cache:       make(map[string]cacheEntry),
		tools: []Tool{
			{
				Name:        "fetch_url",
				Description: "Fetch a URL and return rendered HTML (JavaScript executed). Supports following redirects. Use grep to filter lines, or start_line/end_line to return a specific portion of the page.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch"},"wait":{"type":"number","description":"Wait time in seconds after load (default 2)"},"grep":{"type":"string","description":"Regex pattern to filter returned HTML lines"},"start_line":{"type":"number","description":"Start line number (1-indexed) to return"},"end_line":{"type":"number","description":"End line number (1-indexed, inclusive) to return"}},"required":["url"]}`),
			},
			{
				Name:        "get_links",
				Description: "Extract all links from HTML content",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"html":{"type":"string","description":"HTML content to parse"},"base_url":{"type":"string","description":"Base URL for resolving relative links"}},"required":["html"]}`),
			},
		},
	}
}

func (s *WebScraperServer) Name() string {
	return "webscraper"
}

func (s *WebScraperServer) Tools() []Tool {
	return s.tools
}

func (s *WebScraperServer) isURLAllowed(rawURL string) bool {
	if len(s.allowedURLs) == 0 {
		return true
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	for _, allowed := range s.allowedURLs {
		allowedParsed, err := url.Parse(allowed)
		if err != nil {
			continue
		}

		if parsed.Host == allowedParsed.Host || strings.HasSuffix(parsed.Host, "."+allowedParsed.Host) {
			return true
		}
		if strings.HasPrefix(parsed.Path, allowedParsed.Path) && parsed.Host == allowedParsed.Host {
			return true
		}
	}
	return false
}

func (s *WebScraperServer) checkAndMaybeAddURL(rawURL string) error {
	if !s.isURLAllowed(rawURL) {
		if s.autoConfirm {
			s.allowedURLs = append(s.allowedURLs, rawURL)
			log.Printf("[WEBSCRAPER] Auto-confirmed restricted URL: %s", rawURL)
			return nil
		}
		return fmt.Errorf("URL not allowed: %s", rawURL)
	}
	return nil
}

func (s *WebScraperServer) CallTool(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
	switch name {
	case "fetch_url":
		return s.fetchURL(arguments)
	case "get_links":
		return s.getLinks(arguments)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (s *WebScraperServer) fetchURL(args map[string]interface{}) (string, error) {
	rawURL, ok := args["url"].(string)
	if !ok {
		return "", fmt.Errorf("url is required")
	}

	if err := s.checkAndMaybeAddURL(rawURL); err != nil {
		return "", err
	}

	waitSec := 2
	if w, ok := args["wait"].(float64); ok {
		waitSec = int(w)
	}

	grepPattern, _ := args["grep"].(string)
	startLine, hasStart := args["start_line"].(float64)
	endLine, hasEnd := args["end_line"].(float64)

	// Check cache
	s.cacheMu.Lock()
	if entry, exists := s.cache[rawURL]; exists && time.Now().Before(entry.expiresAt) {
		htmlContent := entry.html
		s.cacheMu.Unlock()
		return s.processContent(htmlContent, grepPattern, startLine, endLine, hasStart, hasEnd)
	}
	s.cacheMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := []chromedp.ExecAllocatorOption{
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"),
	}

	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var htmlContent string
	err := chromedp.Run(taskCtx,
		chromedp.Navigate(rawURL),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.Sleep(time.Duration(waitSec)*time.Second),
		chromedp.OuterHTML("html", &htmlContent, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %v", err)
	}

	// Cache the result for 1 minute
	s.cacheMu.Lock()
	s.cache[rawURL] = cacheEntry{
		html:      htmlContent,
		expiresAt: time.Now().Add(1 * time.Minute),
	}
	s.cacheMu.Unlock()

	return s.processContent(htmlContent, grepPattern, startLine, endLine, hasStart, hasEnd)
}

func (s *WebScraperServer) processContent(htmlContent, grepPattern string, startLine, endLine float64, hasStart, hasEnd bool) (string, error) {
	// Apply line range first to slice the raw HTML
	if hasStart || hasEnd {
		if !hasStart {
			startLine = 1
		}
		if !hasEnd {
			lines := strings.Split(htmlContent, "\n")
			endLine = float64(len(lines))
		}
		htmlContent = s.applyLineRange(htmlContent, startLine, endLine)
	}

	// Apply grep filter if provided
	if grepPattern != "" {
		matched, err := s.applyGrep(htmlContent, grepPattern)
		if err != nil {
			return "", err
		}
		htmlContent = matched
	}

	return htmlContent, nil
}

func (s *WebScraperServer) applyGrep(htmlContent, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid grep pattern: %v", err)
	}

	lines := strings.Split(htmlContent, "\n")
	var matched []string
	for _, line := range lines {
		if re.MatchString(line) {
			matched = append(matched, line)
		}
	}

	if len(matched) == 0 {
		return "No matches found for pattern: " + pattern, nil
	}

	return strings.Join(matched, "\n"), nil
}

func (s *WebScraperServer) applyLineRange(content string, start, end float64) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Convert to 0-indexed
	startIdx := int(start) - 1
	endIdx := int(end)

	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx > totalLines {
		endIdx = totalLines
	}
	if startIdx >= endIdx {
		return ""
	}

	return strings.Join(lines[startIdx:endIdx], "\n")
}

func (s *WebScraperServer) getLinks(args map[string]interface{}) (string, error) {
	htmlContent, ok := args["html"].(string)
	if !ok {
		return "", fmt.Errorf("html is required")
	}

	baseURL, _ := args["base_url"].(string)

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %v", err)
	}

	var links []string
	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			for _, attr := range node.Attr {
				if attr.Key == "href" {
					link := attr.Val
					if baseURL != "" && !strings.HasPrefix(link, "http") {
						base, _ := url.Parse(baseURL)
						abs, _ := url.Parse(link)
						link = base.ResolveReference(abs).String()
					}
					links = append(links, link)
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(doc)

	if len(links) == 0 {
		return "No links found", nil
	}

	return strings.Join(links, "\n"), nil
}

func (s *WebScraperServer) AddPath(ctx context.Context, path string) error {
	return s.AddURL(ctx, path)
}

func (s *WebScraperServer) AddURL(ctx context.Context, url string) error {
	for _, u := range s.allowedURLs {
		if u == url {
			return nil
		}
	}
	s.allowedURLs = append(s.allowedURLs, url)
	return nil
}

func (s *WebScraperServer) AllowedPaths() []string {
	return s.allowedURLs
}

func (s *WebScraperServer) TempAddPath(path string) {
	s.allowedURLs = append(s.allowedURLs, path)
}

func (s *WebScraperServer) RemovePath(ctx context.Context, path string) error {
	for i, p := range s.allowedURLs {
		if p == path {
			s.allowedURLs = append(s.allowedURLs[:i], s.allowedURLs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("path not found: %s", path)
}

func (s *WebScraperServer) RemoveURL(ctx context.Context, url string) error {
	return s.RemovePath(ctx, url)
}

func (s *WebScraperServer) Close() error {
	return s.Teardown()
}

func (s *WebScraperServer) Teardown() error {
	if s.browserCancel != nil {
		s.browserCancel()
	}
	return nil
}
