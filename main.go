package main

import (
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

var verbose bool

func vprint(format string, args ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[findrss] "+format+"\n", args...)
	}
}

func Find(urlStr string) (string, error) {
	urlStr = convertSubstackProfile(urlStr)
	feedURL, err := findFeedAtURL(urlStr)
	if err != nil {
		return "", err
	}
	if feedURL != "" {
		return feedURL, nil
	}
	return tryBlogFallbacks(urlStr)
}

func convertSubstackProfile(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}
	if u.Host == "substack.com" || u.Host == "www.substack.com" {
		path := strings.TrimPrefix(u.Path, "/")
		if strings.HasPrefix(path, "@") {
			username := strings.TrimPrefix(path, "@")
			vprint("Detected Substack profile, fetching publication for @%s", username)
			if pubURL := fetchSubstackPublication(urlStr); pubURL != "" {
				vprint("Converted Substack profile to: %s", pubURL)
				return pubURL
			}
			vprint("Falling back to default substack URL pattern")
			return fmt.Sprintf("https://%s.substack.com", username)
		}
	}
	return urlStr
}

func fetchSubstackPublication(profileURL string) string {
	vprint("Request: GET %s (timeout: %v)", profileURL, SubstackTimeout)
	client := &http.Client{Timeout: SubstackTimeout}
	req, _ := http.NewRequest("GET", profileURL, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		vprint("Request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return ""
	}
	var pubURL string
	doc.Find("a[href*='substack.com']").Each(func(i int, s *goquery.Selection) {
		if pubURL != "" {
			return
		}
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		if strings.Contains(href, "substack.com") && !strings.Contains(href, "/@") {
			if u, err := url.Parse(href); err == nil {
				parts := strings.Split(u.Host, ".")
				if len(parts) >= 2 && parts[len(parts)-2] == "substack" {
					pubURL = fmt.Sprintf("https://%s", u.Host)
					return
				}
			}
		}
	})
	return pubURL
}

func tryBlogFallbacks(urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(u.Host, "blog.") {
		blogURL := *u
		blogURL.Host = "blog." + u.Host
		vprint("Trying blog subdomain fallback: %s", blogURL.String())
		if feedURL, err := findFeedAtURL(blogURL.String()); err == nil && feedURL != "" {
			vprint("Found feed via blog subdomain")
			return feedURL, nil
		}
	}
	if !strings.HasPrefix(u.Path, "/blog") {
		blogPathURL := *u
		blogPathURL.Path = "/blog/"
		vprint("Trying /blog/ path fallback: %s", blogPathURL.String())
		if feedURL, err := findFeedAtURL(blogPathURL.String()); err == nil && feedURL != "" {
			vprint("Found feed via /blog/ path")
			return feedURL, nil
		}
	}
	return "", nil
}

func findFeedAtURL(urlStr string) (string, error) {
	vprint("Request: GET %s (timeout: %v)", urlStr, MainTimeout)
	client := &http.Client{Timeout: MainTimeout}
	req, _ := http.NewRequest("GET", urlStr, nil)
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		vprint("Request failed: %v", err)
		return "", err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}
	baseURL := urlStr
	doc.Find("base[href]").Each(func(i int, s *goquery.Selection) {
		if href, exists := s.Attr("href"); exists {
			baseURL = href
		}
	})
	var feedURL string
	doc.Find("head link[rel='alternate'][type='application/atom+xml'], head link[rel='alternate'][type='application/rss+xml']").Each(func(i int, s *goquery.Selection) {
		if feedURL != "" {
			return
		}
		href, exists := s.Attr("href")
		if !exists {
			return
		}
		title, _ := s.Attr("title")
		if strings.Contains(strings.ToLower(href), "comments") || strings.Contains(strings.ToLower(title), "comments feed") {
			return
		}
		absoluteURL, _ := url.Parse(baseURL)
		relativeURL, _ := url.Parse(href)
		feedURL = absoluteURL.ResolveReference(relativeURL).String()
	})
	if feedURL != "" {
		vprint("Found feed via autodiscovery: %s", feedURL)
		return feedURL, nil
	}
	return tryFeedSuffixesParallel(baseURL)
}

func tryFeedSuffixesParallel(baseURL string) (string, error) {
	suffixes := []string{
		"feed", "feed/", "rss", "atom", "feed.xml",
		"/feed", "/feed/", "/rss", "/atom", "/feed.xml",
		"index.atom", "index.rss", "index.xml", "atom.xml", "rss.xml",
		"/index.atom", "/index.rss", "/index.xml", "/atom.xml", "/rss.xml",
		".rss", "/.rss", "?rss=1", "?feed=rss2",
	}
	baseURLObj, _ := url.Parse(baseURL)
	type result struct {
		url string
	}
	results := make(chan result, 1)
	var wg sync.WaitGroup
	var once sync.Once
	vprint("Trying %d common feed URL patterns concurrently", len(suffixes))
	for _, suffix := range suffixes {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			suffixURL, _ := url.Parse(s)
			testURL := baseURLObj.ResolveReference(suffixURL).String()
			parser := gofeed.NewParser()
			feed, err := parser.ParseURL(testURL)
			if err == nil && feed != nil {
				once.Do(func() {
					vprint("Found valid feed: %s", testURL)
					results <- result{url: testURL}
				})
			}
		}(suffix)
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	res := <-results
	if res.url != "" {
		return res.url, nil
	}
	return "", nil
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
	feedURL, err := Find(urlToSearch)
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
