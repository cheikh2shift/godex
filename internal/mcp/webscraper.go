package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"golang.org/x/net/html"
)

type WebScraperServer struct {
	allowedURLs   []string
	tools         []Tool
	browserCtx    context.Context
	browserCancel context.CancelFunc
	autoConfirm   bool
}

func NewWebScraperServer(allowedURLs []string, autoConfirm bool) *WebScraperServer {
	if len(allowedURLs) == 0 {
		allowedURLs = []string{}
	}

	return &WebScraperServer{
		allowedURLs: allowedURLs,
		autoConfirm: autoConfirm,
		tools: []Tool{
			{
				Name:        "fetch_url",
				Description: "Fetch a URL and return rendered HTML (JavaScript executed). Supports following redirects.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch"},"wait":{"type":"number","description":"Wait time in seconds after load (default 2)"}},"required":["url"]}`),
			},
			{
				Name:        "search_html",
				Description: "Search HTML content for text or HTML elements using CSS selector or text search",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"html":{"type":"string","description":"HTML content to search"},"selector":{"type":"string","description":"CSS selector to match"},"text":{"type":"string","description":"Text pattern to search for"}},"required":["html"]}`),
			},
			{
				Name:        "get_links",
				Description: "Extract all links from HTML content",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"html":{"type":"string","description":"HTML content to parse"},"base_url":{"type":"string","description":"Base URL for resolving relative links"}},"required":["html"]}`),
			},
			/*{
				Name:        "click_element",
				Description: "Click an element on a rendered page and return the new HTML (requires active browser context)",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"selector":{"type":"string","description":"CSS selector to click"},"wait":{"type":"number","description":"Wait time in seconds after click (default 2)"}},"required":["selector"]}`),
			},*/
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
	case "search_html":
		return s.searchHTML(arguments)
	case "get_links":
		return s.getLinks(arguments)
	case "click_element":
		return s.clickElement(ctx, arguments)
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

	return htmlContent, nil
}

func (s *WebScraperServer) searchHTML(args map[string]interface{}) (string, error) {
	htmlContent, ok := args["html"].(string)
	if !ok {
		return "", fmt.Errorf("html is required")
	}

	selector, _ := args["selector"].(string)
	textPattern, _ := args["text"].(string)

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %v", err)
	}

	var results []string

	if selector != "" {
		results = s.findBySelector(doc, selector)
	}

	if textPattern != "" {
		results = append(results, s.findByText(doc, textPattern)...)
	}

	if selector == "" && textPattern == "" {
		results = s.findByText(doc, ".")
	}

	if len(results) == 0 {
		return "No matches found", nil
	}

	return strings.Join(results, "\n---\n"), nil
}

func (s *WebScraperServer) findBySelector(n *html.Node, selector string) []string {
	var results []string

	selector = strings.TrimPrefix(selector, ".")

	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			for _, attr := range node.Attr {
				if attr.Key == "class" {
					classes := strings.Fields(attr.Val)
					for _, c := range classes {
						if c == selector || strings.Contains(c, selector) {
							if text := s.getTextContent(node); text != "" {
								results = append(results, text)
							}
						}
					}
				}
				if attr.Key == "id" && attr.Val == selector {
					if text := s.getTextContent(node); text != "" {
						results = append(results, text)
					}
				}
			}
			if node.Data == selector {
				if text := s.getTextContent(node); text != "" {
					results = append(results, text)
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(n)
	return results
}

func (s *WebScraperServer) findByText(n *html.Node, pattern string) []string {
	var results []string
	pattern = strings.ToLower(pattern)

	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text := strings.TrimSpace(node.Data)
			if text != "" && (pattern == "." || strings.Contains(strings.ToLower(text), pattern)) {
				results = append(results, text)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}

	walk(n)
	return results
}

func (s *WebScraperServer) getTextContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
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

func (s *WebScraperServer) clickElement(ctx context.Context, args map[string]interface{}) (string, error) {
	selector, ok := args["selector"].(string)
	if !ok {
		return "", fmt.Errorf("selector is required")
	}

	waitSec := 2
	if w, ok := args["wait"].(float64); ok {
		waitSec = int(w)
	}

	allocCtx, cancel := chromedp.NewExecAllocator(ctx,
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	defer cancel()

	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var htmlContent string
	err := chromedp.Run(taskCtx,
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.Sleep(time.Duration(waitSec)*time.Second),
		chromedp.OuterHTML("html", &htmlContent, chromedp.ByQuery),
	)
	if err != nil {
		return "", fmt.Errorf("failed to click element: %v", err)
	}

	return htmlContent, nil
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

func (s *WebScraperServer) TempAddPath(path string) {
	s.AddPath(context.Background(), path)
}

func (s *WebScraperServer) RemovePath(ctx context.Context, path string) error {
	return s.RemoveURL(ctx, path)
}

func (s *WebScraperServer) RemoveURL(ctx context.Context, url string) error {
	for i, u := range s.allowedURLs {
		if u == url {
			s.allowedURLs = append(s.allowedURLs[:i], s.allowedURLs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("URL not found: %s", url)
}

func (s *WebScraperServer) AllowedPaths() []string {
	return s.allowedURLs
}

func (s *WebScraperServer) Close() error {
	if s.browserCancel != nil {
		s.browserCancel()
	}
	return nil
}
