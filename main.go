package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

// UserAgent represents the User-Agent header to use in requests
const UserAgent = "FeedFinder/1.0"

// Find attempts to discover RSS or Atom feeds for a given URL
func Find(urlStr string) (string, error) {
	// Set up the HTTP client with appropriate headers
	client := &http.Client{}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)

	// Make the HTTP request
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Parse the HTML document
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}

	// Determine base URL for relative URLs
	baseURL := urlStr
	doc.Find("base[href]").Each(func(i int, s *goquery.Selection) {
		if href, exists := s.Attr("href"); exists {
			baseURL = href
		}
	})

	// Look for feed links in the HTML head
	// Prioritize Atom over RSS
	var feedURL string
	doc.Find("head link[rel='alternate'][type='application/atom+xml'], head link[rel='alternate'][type='application/rss+xml']").Each(func(i int, s *goquery.Selection) {
		if feedURL != "" {
			return // Already found a feed
		}

		href, exists := s.Attr("href")
		if !exists {
			return
		}

		// Skip comments feeds
		title, _ := s.Attr("title")
		if strings.Contains(strings.ToLower(href), "comments") || 
		   strings.Contains(strings.ToLower(title), "comments feed") {
			return
		}

		// Join with base URL to get absolute URL
		absoluteURL, err := url.Parse(baseURL)
		if err != nil {
			return
		}
		
		relativeURL, err := url.Parse(href)
		if err != nil {
			return
		}
		
		feedURL = absoluteURL.ResolveReference(relativeURL).String()
	})

	// If we found a feed through autodiscovery, return it
	if feedURL != "" {
		return feedURL, nil
	}

	// No usable autodiscovery links in the meta, try some heuristics
	suffixes := []string{
		"feed", "feed/", "rss", "atom", "feed.xml",
		"/feed", "/feed/", "/rss", "/atom", "/feed.xml",
		"index.atom", "index.rss", "index.xml", "atom.xml", "rss.xml",
		"/index.atom", "/index.rss", "/index.xml", "/atom.xml", "/rss.xml",
		".rss", "/.rss", "?rss=1", "?feed=rss2",
	}

	parser := gofeed.NewParser()
	baseURLObj, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}

	for _, suffix := range suffixes {
		suffixURL, err := url.Parse(suffix)
		if err != nil {
			continue
		}
		
		testURL := baseURLObj.ResolveReference(suffixURL).String()
		
		// Try to parse as a feed
		feed, err := parser.ParseURL(testURL)
		if err == nil && feed != nil {
			return testURL, nil
		}
	}

	return "", nil // No feed found
}

func main() {
	// Check if a URL was provided as a command-line argument
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <url>\n", os.Args[0])
		os.Exit(1)
	}

	// Get the URL from command-line arguments
	urlToSearch := os.Args[1]

	// Try to find a feed for the URL
	feedURL, err := Find(urlToSearch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	// Print the feed URL if found, or an error message if not
	if feedURL != "" {
		fmt.Println(feedURL)
	} else {
		fmt.Fprintf(os.Stderr, "No feed found for %s\n", urlToSearch)
		os.Exit(1)
	}
}
