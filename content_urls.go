package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-rails/contentkit/contenturl"
)

// postRoute is the posts' page: /p/{CODE}/{slug}. Channels keep /c/{handle}.
const postRoute = "p"

// newURLRouter canonicalizes content page requests before the SPA: a stale
// or missing slug or another code spelling is a 301, a removed post a 410.
func newURLRouter(posts *postAPI, cfg Config) (*contenturl.Router, error) {
	return contenturl.NewRouter(posts.urls, contenturl.RouterOptions{
		Routes:     contenturl.Routes{kindPost: postRoute},
		BaseURL:    cfg.PublicURL,
		Visibility: posts.urlVisibility,
		Gone:       spaPage(http.StatusGone, "", nil),
	})
}

// urlVisibility: a published post on a live channel is served; a deleted
// post or channel is gone (the Gone page is still the app, where a deleted
// post's buyers read it); drafts and publishing posts are like unknown codes
// (their editors open them in the app).
func (api *postAPI) urlVisibility(r *http.Request, l contenturl.Link) (contenturl.Visibility, error) {
	if l.ContentKind != kindPost {
		return contenturl.Hidden, nil
	}
	var state string
	var gone bool
	err := api.pool.QueryRow(r.Context(), `SELECT p.state, p.deleted_at IS NOT NULL OR ch.deleted_at IS NOT NULL FROM `+api.table+` p
		JOIN `+api.channels.table+` ch ON ch.id=p.channel_id WHERE p.id=$1`, l.ContentID).Scan(&state, &gone)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return contenturl.Hidden, nil
	case err != nil:
		return contenturl.Hidden, err
	case gone:
		return contenturl.Gone, nil
	case state == statePublished:
		return contenturl.Visible, nil
	}
	return contenturl.Hidden, nil
}

// spaPage serves the app shell with status. A canonical content page gets
// its canonical URL and title in the head for crawlers and link previews.
func spaPage(status int, base string, posts *postAPI) http.Handler {
	index, err := frontendFiles.ReadFile("frontend/dist/index.html")
	if err != nil {
		panic(err) // embedded at build time
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := index
		if d, ok := contenturl.FromContext(r.Context()); ok && posts != nil {
			var title string
			if err := posts.pool.QueryRow(r.Context(), `SELECT title FROM `+posts.table+` WHERE id=$1`, d.Link.ContentID).Scan(&title); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			page = withHead(index, base+d.Path, title)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(page)
	})
}

// withHead adds a page's canonical link and Open Graph tags to the shell.
func withHead(index []byte, url, title string) []byte {
	tags := `<link rel="canonical" href="` + html.EscapeString(url) + `" />` +
		`<meta property="og:url" content="` + html.EscapeString(url) + `" />` +
		`<meta property="og:type" content="article" />`
	if title != "" {
		tags += `<meta property="og:title" content="` + html.EscapeString(title) + `" />`
	}
	return bytes.Replace(index, []byte("</head>"), []byte(tags+"</head>"), 1)
}

// backfillURLs registers every post's code and slug. Put is idempotent, so
// it is safe to run again.
func backfillURLs(ctx context.Context, out io.Writer) error {
	cfg, pool, err := openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	n, err := backfillPostURLs(ctx, cfg, pool)
	if err == nil {
		fmt.Fprintf(out, "registered %d posts\n", n)
	}
	return err
}

func backfillPostURLs(ctx context.Context, cfg Config, pool *pgxpool.Pool) (int, error) {
	urls, err := contenturl.New(contenturl.Options{Pool: pool, Schema: contentSchema(cfg), Tenant: tenant})
	if err != nil {
		return 0, err
	}
	table := pgx.Identifier{appSchema(cfg), "posts"}.Sanitize()
	n, last := 0, "00000000-0000-0000-0000-000000000000"
	for {
		rows, err := pool.Query(ctx, `SELECT id::text, title FROM `+table+` WHERE id > $1::uuid ORDER BY id LIMIT 500`, last)
		if err != nil {
			return n, err
		}
		var entries []contenturl.Entry
		var id, title string
		if _, err = pgx.ForEachRow(rows, []any{&id, &title}, func() error {
			entries = append(entries, contenturl.Entry{ContentRef: postRef(id), Title: title})
			return nil
		}); err != nil {
			return n, err
		}
		if len(entries) == 0 {
			return n, nil
		}
		if _, err = urls.Put(ctx, entries...); err != nil {
			return n, err
		}
		n, last = n+len(entries), id
	}
}
