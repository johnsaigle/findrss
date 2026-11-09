# findrss

Finds RSS and ATOM feeds for a URL. 

## Usage

```sh
# Install
go install github.com/johnsaigle/findrss

# Run
findrss https://cyberinsider.com/
```

## How it works

1. Substack profile URLs (`https://substack.com/@handle`) are mapped to the
   author's primary publication, preferring its custom domain.
2. The page is checked for `<link rel="alternate">` feed autodiscovery.
3. If none is found, common feed paths (`/feed`, `/rss`, `/atom.xml`, ...) are
   probed concurrently. The highest-priority path that parses as a feed wins,
   so results are deterministic regardless of response order.
4. If still nothing, `blog.<host>` and `<host>/blog/` are tried concurrently,
   preferring the subdomain.
