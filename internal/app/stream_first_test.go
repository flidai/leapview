package app

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"
)

var literalUpdatesActionPattern = regexp.MustCompile(`@get\('([^']+)'`)

func pageUpdatesURL(pageBody string) string {
	// Read the actual main initialization attribute before looking for its
	// literal action. Other elements and lifecycle handlers can also use @get.
	tokens := xhtml.NewTokenizer(strings.NewReader(pageBody))
	for {
		switch tokens.Next() {
		case xhtml.ErrorToken:
			return ""
		case xhtml.StartTagToken:
			token := tokens.Token()
			if token.Data != "main" {
				continue
			}
			for _, attribute := range token.Attr {
				if attribute.Key != "data-init" {
					continue
				}
				if matches := literalUpdatesActionPattern.FindStringSubmatch(attribute.Val); len(matches) == 2 {
					return matches[1]
				}
			}
			return ""
		}
	}
}

func TestPageUpdatesURLReadsMainInitialization(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
	}{
		{"literal", `<main data-init="@get(&#39;/updates?route=data&#39;)"></main>`, "/updates?route=data"},
		{"lifecycle", `<main data-init="el._pagestreamAbort?.abort(); if (!document.hidden) { @get(&#39;/updates?route=data&amp;surface=explore&#39;, {openWhenHidden: true}) }"></main>`, "/updates?route=data&surface=explore"},
		{"other element", `<header data-init="@get(&#39;/wrong&#39;)"></header><main data-init="@get(&#39;/updates&#39;)"></main>`, "/updates"},
		{"other attribute", `<main data-on:visibilitychange__document="@get(&#39;/wrong&#39;)" data-init="0"></main>`, ""},
		{"missing initialization", `<main><button data-on:click="@get(&#39;/wrong&#39;)"></button></main>`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := pageUpdatesURL(test.body); got != test.want {
				t.Fatalf("pageUpdatesURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func renderedWithBootstrap(t *testing.T, server *appTestHarness, pageBody, authorization string) string {
	t.Helper()
	return html.UnescapeString(pageBody) + streamBootstrapBody(t, server, pageBody, authorization)
}

func streamBootstrapBody(t *testing.T, server *appTestHarness, pageBody, authorization string) string {
	t.Helper()
	updatesURL := pageUpdatesURL(pageBody)
	if updatesURL == "" {
		t.Fatalf("rendered page did not include literal /updates data-init:\n%s", pageBody)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, updatesURL, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "stream-first-test"})
	rec := newSynchronizedResponseRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		if strings.Contains(rec.BodyString(), "datastar-patch-signals") {
			break
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-timer.C:
			cancel()
			<-done
			t.Fatalf("updates bootstrap did not emit signal patch for %q:\n%s", updatesURL, rec.BodyString())
		}
	}
	cancel()
	<-done
	body := html.UnescapeString(rec.BodyString())
	for _, forbidden := range []string{`"updatesUrl"`, `"routeKey"`, `"csrfToken"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("updates bootstrap leaked %s for %q:\n%s", forbidden, updatesURL, body)
		}
	}
	return body
}
