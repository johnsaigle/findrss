package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

const UserAgent = "FeedFinder/1.0"

// Timeouts
const (
	SubstackTimeout = 10 * time.Second
	MainTimeout     = 15 * time.Second
)

// substackAPIBase is a variable so tests can point it at a local server.
var substackAPIBase = "https://substack.com"

// feedSuffixes are probed, in priority order, when a page has no
// autodiscovery <link>. Earlier entries win when several are valid feeds.
var feedSuffixes = []string{
	"feed", "feed/", "rss", "atom", "feed.xml",
	"/feed", "/feed/", "/rss", "/atom", "/feed.xml",
	"index.atom", "index.rss", "index.xml", "atom.xml", "rss.xml",
	"/index.atom", "/index.rss", "/index.xml", "/atom.xml", "/rss.xml",
	".rss", "/.rss", "?rss=1", "?feed=rss2",
}

var verbose bool

func vprint(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[findrss] "+format+"\n", args...)
	}
}

// userAgentTransport sets the User-Agent on every request, including those
// made by gofeed and redirects.
type userAgentTransport struct{ base http.RoundTripper }

func (t userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", UserAgent)
	return t.base.RoundTrip(req)
}

var httpClient = &http.Client{
	Timeout:   MainTimeout,
	Transport: userAgentTransport{base: http.DefaultTransport},
}

// Find returns the feed URL for urlStr, or "" if none was found.
//
// Lookup order: the URL itself (autodiscovery, then common feed paths), then
// the blog.<host> subdomain and the /blog/ path. The fallbacks are probed
// concurrently, but the subdomain is preferred when both have a feed.
func Find(ctx context.Context, urlStr string) (string, error) {
	urlStr = convertSubstackProfile(ctx, urlStr)
	feedURL, err := findFeedAtURL(ctx, urlStr)
	if err != nil {
		return "", err
	}
	if feedURL != "" {
		return feedURL, nil
	}
	return tryBlogFallbacks(ctx, urlStr)
}

// firstInOrder runs every probe concurrently and returns the result of the
// lowest-indexed probe that yields a non-empty string. It returns as soon as
// that answer is certain (every earlier probe has finished empty), and
// cancels the remaining probes.
func firstInOrder(ctx context.Context, probes []func(context.Context) string) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		index int
		value string
	}
	outcomes := make(chan outcome, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes <- outcome{index: i, value: probe(ctx)}
		}()
	}
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	done := make([]bool, len(probes))
	values := make([]string, len(probes))
	next := 0 // lowest index whose outcome is still unknown
	for o := range outcomes {
		done[o.index] = true
		values[o.index] = o.value
		for next < len(probes) && done[next] {
			if values[next] != "" {
				return values[next]
			}
			next++
		}
	}
	return ""
}

// substackHandle returns the handle for a substack.com/@handle profile URL.
func substackHandle(u *url.URL) (string, bool) {
	if u.Host != "substack.com" && u.Host != "www.substack.com" {
		return "", false
	}
	path := strings.Trim(u.Path, "/")
	if !strings.HasPrefix(path, "@") {
		return "", false
	}
	handle := strings.TrimPrefix(path, "@")
	if handle == "" || strings.Contains(handle, "/") {
		return "", false
	}
	return handle, true
}

// convertSubstackProfile maps a Substack profile URL (which has no feed) to
// the author's primary publication, preferring its custom domain.
func convertSubstackProfile(ctx context.Context, urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}
	handle, ok := substackHandle(u)
	if !ok {
		return urlStr
	}
	vprint("Detected Substack profile, looking up publication for @%s", handle)
	if pubURL := fetchSubstackPublication(ctx, handle); pubURL != "" {
		vprint("Converted Substack profile to: %s", pubURL)
		return pubURL
	}
	vprint("Falling back to default substack URL pattern")
	return fmt.Sprintf("https://%s.substack.com", handle)
}

type substackProfile struct {
	PrimaryPublication *struct {
		Subdomain    string `json:"subdomain"`
		CustomDomain string `json:"custom_domain"`
	} `json:"primaryPublication"`
}

func fetchSubstackPublication(ctx context.Context, handle string) string {
	ctx, cancel := context.WithTimeout(ctx, SubstackTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("%s/api/v1/user/%s/public_profile", substackAPIBase, url.PathEscape(handle))
	vprint("Request: GET %s (timeout: %v)", endpoint, SubstackTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		vprint("Request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		vprint("Substack profile lookup returned %s", resp.Status)
		return ""
	}

	var profile substackProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		vprint("Could not decode Substack profile: %v", err)
		return ""
	}
	pub := profile.PrimaryPublication
	switch {
	case pub == nil:
		return ""
	case pub.CustomDomain != "":
		return "https://" + pub.CustomDomain
	case pub.Subdomain != "":
		return fmt.Sprintf("https://%s.substack.com", pub.Subdomain)
	default:
		return ""
	}
}

func tryBlogFallbacks(ctx context.Context, urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	var candidates []string
	if !strings.HasPrefix(u.Host, "blog.") {
		blogURL := *u
		blogURL.Host = "blog." + u.Host
		candidates = append(candidates, blogURL.String())
	}
	if !strings.HasPrefix(u.Path, "/blog") {
		blogPathURL := *u
		blogPathURL.Path = "/blog/"
		blogPathURL.RawQuery = ""
		candidates = append(candidates, blogPathURL.String())
	}

	probes := make([]func(context.Context) string, len(candidates))
	for i, candidate := range candidates {
		probes[i] = func(ctx context.Context) string {
			vprint("Trying blog fallback: %s", candidate)
			feedURL, err := findFeedAtURL(ctx, candidate)
			if err != nil {
				return ""
			}
			return feedURL
		}
	}
	feedURL := firstInOrder(ctx, probes)
	if feedURL != "" {
		vprint("Found feed via blog fallback: %s", feedURL)
	}
	return feedURL, nil
}

func findFeedAtURL(ctx context.Context, urlStr string) (string, error) {
	vprint("Request: GET %s (timeout: %v)", urlStr, MainTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		vprint("Request failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}

	// Resolve relative links against where we actually ended up, not where
	// we started: e.g. foo.substack.com redirects to a custom domain.
	base := resp.Request.URL
	if href, ok := doc.Find("base[href]").Last().Attr("href"); ok {
		if ref, err := url.Parse(href); err == nil {
			base = base.ResolveReference(ref)
		}
	}

	var feedURL string
	doc.Find("head link[rel='alternate'][type='application/atom+xml'], head link[rel='alternate'][type='application/rss+xml']").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, exists := s.Attr("href")
		if !exists {
			return true
		}
		title, _ := s.Attr("title")
		if strings.Contains(strings.ToLower(href), "comments") || strings.Contains(strings.ToLower(title), "comments feed") {
			return true
		}
		ref, err := url.Parse(href)
		if err != nil {
			return true
		}
		feedURL = base.ResolveReference(ref).String()
		return false
	})
	if feedURL != "" {
		vprint("Found feed via autodiscovery: %s", feedURL)
		return feedURL, nil
	}
	return tryFeedSuffixesParallel(ctx, base), nil
}

// tryFeedSuffixesParallel probes common feed paths concurrently. The result
// is deterministic: the highest-priority suffix that parses as a feed wins.
func tryFeedSuffixesParallel(ctx context.Context, base *url.URL) string {
	vprint("Trying %d common feed URL patterns concurrently", len(feedSuffixes))
	probes := make([]func(context.Context) string, len(feedSuffixes))
	for i, suffix := range feedSuffixes {
		probes[i] = func(ctx context.Context) string {
			ref, err := url.Parse(suffix)
			if err != nil {
				return ""
			}
			testURL := base.ResolveReference(ref).String()
			parser := gofeed.NewParser()
			parser.Client = httpClient
			parser.UserAgent = UserAgent
			feed, err := parser.ParseURLWithContext(testURL, ctx)
			if err != nil || feed == nil {
				return ""
			}
			vprint("Found valid feed: %s", testURL)
			return testURL
		}
	}
	return firstInOrder(ctx, probes)
}

func main() {
	flag.BoolVar(&verbose, "v", false, "Enable verbose mode")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-v] <url>\n", os.Args[0])
		os.Exit(1)
	}

	if verbose {
		vprint("Verbose mode enabled")
		vprint("Main request timeout: %v", MainTimeout)
		vprint("Substack profile timeout: %v", SubstackTimeout)
	}

	urlToSearch := args[0]
	feedURL, err := Find(context.Background(), urlToSearch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
	if feedURL != "" {
		fmt.Println(feedURL)
	} else {
		fmt.Fprintf(os.Stderr, "No feed found for %s\n", urlToSearch)
		os.Exit(1)
	}
}
