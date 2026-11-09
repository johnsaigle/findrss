package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

const atomFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>t</title><id>x</id><updated>2026-01-01T00:00:00Z</updated></feed>`

func TestFirstInOrderPrefersLowestIndex(t *testing.T) {
	probes := []func(context.Context) string{
		func(context.Context) string { time.Sleep(30 * time.Millisecond); return "" },
		func(context.Context) string { time.Sleep(20 * time.Millisecond); return "second" },
		func(context.Context) string { return "third" }, // fastest, but lower priority
	}
	if got := firstInOrder(context.Background(), probes); got != "second" {
		t.Fatalf("got %q, want %q", got, "second")
	}
}

func TestFirstInOrderDoesNotWaitForLowerPriorityProbes(t *testing.T) {
	cancelled := make(chan struct{})
	probes := []func(context.Context) string{
		func(context.Context) string { return "first" },
		func(ctx context.Context) string { <-ctx.Done(); close(cancelled); return "" },
	}
	if got := firstInOrder(context.Background(), probes); got != "first" {
		t.Fatalf("got %q", got)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("remaining probe was not cancelled")
	}
}

func TestFirstInOrderAllEmpty(t *testing.T) {
	probes := []func(context.Context) string{
		func(context.Context) string { return "" },
		func(context.Context) string { return "" },
	}
	if got := firstInOrder(context.Background(), probes); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstackHandle(t *testing.T) {
	cases := map[string]string{
		"https://substack.com/@contraptions":      "contraptions",
		"https://www.substack.com/@contraptions/": "contraptions",
		"https://substack.com/@":                  "",
		"https://substack.com/@a/notes":           "",
		"https://contraptions.substack.com/":      "",
		"https://example.com/@contraptions":       "",
	}
	for in, want := range cases {
		u, _ := url.Parse(in)
		got, ok := substackHandle(u)
		if got != want || ok != (want != "") {
			t.Errorf("substackHandle(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestConvertSubstackProfileUsesCustomDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/user/contraptions/public_profile":
			fmt.Fprint(w, `{"primaryPublication":{"subdomain":"contraptions","custom_domain":"contraptions.venkateshrao.com"}}`)
		case "/api/v1/user/plain/public_profile":
			fmt.Fprint(w, `{"primaryPublication":{"subdomain":"plainpub","custom_domain":null}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := substackAPIBase
	substackAPIBase = srv.URL
	defer func() { substackAPIBase = old }()

	ctx := context.Background()
	cases := map[string]string{
		"https://substack.com/@contraptions": "https://contraptions.venkateshrao.com",
		"https://substack.com/@plain":        "https://plainpub.substack.com",
		"https://substack.com/@unknown":      "https://unknown.substack.com", // API miss: guess
		"https://example.com/":               "https://example.com/",         // untouched
	}
	for in, want := range cases {
		if got := convertSubstackProfile(ctx, in); got != want {
			t.Errorf("convertSubstackProfile(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindFeedAtURLAutodiscoveryResolvesAgainstFinalURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/moved/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/moved/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<html><head>
<link rel="alternate" type="application/rss+xml" title="Comments Feed" href="comments.xml">
<link rel="alternate" type="application/atom+xml" href="atom.xml">
</head></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := findFeedAtURL(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/moved/atom.xml"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFindFeedAtURLSuffixProbePrefersEarlierSuffix(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head></head></html>`)
		case "/rss": // higher priority than /atom.xml, but slower
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, atomFeed)
		case "/atom.xml":
			fmt.Fprint(w, atomFeed)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := findFeedAtURL(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/rss"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBlogPathFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blog/":
			fmt.Fprint(w, `<html><head><link rel="alternate" type="application/rss+xml" href="/blog/index.xml"></head></html>`)
		case "/":
			fmt.Fprint(w, `<html><head></head></html>`)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// blog.127.0.0.1:port does not resolve, so only the /blog/ path can win.
	got, err := Find(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/blog/index.xml"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
