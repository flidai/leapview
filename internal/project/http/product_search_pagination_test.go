package http

import (
	"context"
	"encoding/json"
	"html"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Use the catalog's real pagination primitive so continuations retain its
// signed query, page-size, snapshot, and principal bindings.
type cursorProductSearchCatalog struct {
	items    []projectcatalog.Result
	requests []projectcatalog.SearchRequest
}

func (f *cursorProductSearchCatalog) Search(ctx context.Context, request projectcatalog.SearchRequest) (projectcatalog.Page, error) {
	f.requests = append(f.requests, request)
	page, err := access.AuthorizeAndPaginate(ctx, f.items, access.DiscoveryRequest{
		Collection: "project-catalog.search", PrincipalID: request.PrincipalID,
		SecurityContext: "test-authority", Snapshot: "test-snapshot",
		Query: request.Query, Filter: request.Domain, Limit: request.Limit, Cursor: request.Cursor,
	}, func(context.Context, projectcatalog.Result) (bool, error) { return true, nil })
	return projectcatalog.Page{Items: page.Items, NextCursor: page.NextCursor}, err
}

func TestProductSearchContinuationKeepsEveryAuthorizedResult(t *testing.T) {
	for _, format := range []string{"json", "html"} {
		for _, typed := range []bool{false, true} {
			name := format + "/session"
			if typed {
				name = format + "/typed-credential"
			}
			t.Run(name, func(t *testing.T) {
				projectID := projectgraph.ResourceID("project:test")
				catalog := &cursorProductSearchCatalog{}
				credential := access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile}}
				var want []string
				for _, suffix := range []string{"denied-1", "denied-2", "allowed-a", "denied-3", "allowed-b", "allowed-c", "allowed-d"} {
					id := "dashboard:" + suffix
					catalog.items = append(catalog.items, projectcatalog.Result{Ref: projectcatalog.Ref{ID: projectgraph.ResourceID(id), Kind: projectgraph.KindDashboard}, Name: suffix})
					if !typed || strings.HasPrefix(suffix, "allowed-") {
						want = append(want, id)
					}
					if strings.HasPrefix(suffix, "allowed-") {
						resource, err := access.NewResourceRef(projectgraph.ResourceID(id), projectgraph.KindDashboard)
						if err != nil {
							t.Fatal(err)
						}
						pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
						if err != nil {
							t.Fatal(err)
						}
						credential.Token.Permissions = append(credential.Token.Permissions, pair)
					}
				}
				handler := &BrowserHandler{
					SearchCatalog:    catalog,
					ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
					CurrentUser:      func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:ada"}, true },
				}
				if typed {
					handler.CurrentCredential = func(*stdhttp.Request) (access.APICredential, bool) { return credential, true }
				}
				href := "/search?q=dashboard&kind=dashboard&domain=finance&limit=2"
				var got []string
				for pages := 0; href != ""; pages++ {
					if pages >= 10 {
						t.Fatal("search continuation did not finish")
					}
					request := httptest.NewRequest(stdhttp.MethodGet, href, nil)
					if format == "html" {
						request.Header.Set("Accept", "text/html")
					}
					response := httptest.NewRecorder()
					handler.ProductSearch(response, request)
					if response.Code != stdhttp.StatusOK {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					href = ""
					if format == "json" {
						var body struct {
							Items      []productSearchResult `json:"items"`
							NextCursor string                `json:"nextCursor"`
						}
						if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
							t.Fatal(err)
						}
						for _, item := range body.Items {
							got = append(got, item.Reference.ID.String())
						}
						if body.NextCursor != "" {
							values := request.URL.Query()
							values.Set("cursor", body.NextCursor)
							href = "/search?" + values.Encode()
						}
					} else {
						for _, match := range regexp.MustCompile(`<code>(dashboard:[^<]+)</code>`).FindAllStringSubmatch(response.Body.String(), -1) {
							got = append(got, html.UnescapeString(match[1]))
						}
						if match := regexp.MustCompile(`href="([^"]+)">Next results</a>`).FindStringSubmatch(response.Body.String()); match != nil {
							href = html.UnescapeString(match[1])
							next, err := url.Parse(href)
							if err != nil {
								t.Fatal(err)
							}
							values := next.Query()
							if values.Get("q") != "dashboard" || values.Get("domain") != "finance" || values.Get("kind") != "dashboard" || values.Get("limit") != "2" || values.Get("cursor") == "" {
								t.Fatalf("next results lost search context: %s", href)
							}
						}
					}
				}
				if !slices.Equal(got, want) {
					t.Fatalf("results=%v, want every authorized result exactly once: %v", got, want)
				}
				for _, request := range catalog.requests {
					if request.Limit != 2 {
						t.Fatalf("changed cursor-bound catalog limit: %d", request.Limit)
					}
				}
			})
		}
	}
}

type productSearchCatalogFunc func(context.Context, projectcatalog.SearchRequest) (projectcatalog.Page, error)

func (f productSearchCatalogFunc) Search(ctx context.Context, request projectcatalog.SearchRequest) (projectcatalog.Page, error) {
	return f(ctx, request)
}

func TestSearchCatalogPageAuthorizedRejectsRepeatedCursor(t *testing.T) {
	for _, cursor := range []string{"", "repeated-cursor"} {
		t.Run(cursor, func(t *testing.T) {
			calls := 0
			catalog := productSearchCatalogFunc(func(context.Context, projectcatalog.SearchRequest) (projectcatalog.Page, error) {
				calls++
				return projectcatalog.Page{
					Items:      []projectcatalog.Result{{Ref: projectcatalog.Ref{ID: "dashboard:denied", Kind: projectgraph.KindDashboard}}},
					NextCursor: "repeated-cursor",
				}, nil
			})
			credential := &access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile}}
			_, err := searchCatalogPageAuthorized(t.Context(), catalog, projectcatalog.SearchRequest{Limit: 2, Cursor: cursor}, credential, "project:test")
			if err == nil || !strings.Contains(err.Error(), "cursor repeated") || calls > 2 {
				t.Fatalf("calls=%d error=%v, want bounded repeated-cursor failure", calls, err)
			}
		})
	}
}

func TestProductSearchPaletteFillsAuthorizedPage(t *testing.T) {
	projectID := projectgraph.ResourceID("project:test")
	catalog := &cursorProductSearchCatalog{}
	credential := access.APICredential{Token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile}}
	for _, suffix := range []string{"allowed-a", "denied", "allowed-b", "allowed-c"} {
		id := projectgraph.ResourceID("dashboard:" + suffix)
		catalog.items = append(catalog.items, projectcatalog.Result{Ref: projectcatalog.Ref{ID: id, Kind: projectgraph.KindDashboard}, Name: suffix})
		if suffix == "denied" {
			continue
		}
		resource, err := access.NewResourceRef(id, projectgraph.KindDashboard)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, resource)
		if err != nil {
			t.Fatal(err)
		}
		credential.Token.Permissions = append(credential.Token.Permissions, pair)
	}
	handler := &BrowserHandler{
		SearchCatalog:     catalog,
		ResolveProjectID:  func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		CurrentUser:       func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:ada"}, true },
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) { return credential, true },
	}
	response := httptest.NewRecorder()
	handler.ProductSearch(response, httptest.NewRequest(stdhttp.MethodGet, "/search?q=dashboard&limit=2&view=palette", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items      []productSearchResult `json:"items"`
		NextCursor string                `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].Reference.ID != "dashboard:allowed-a" || body.Items[1].Reference.ID != "dashboard:allowed-b" || body.NextCursor != "" {
		t.Fatalf("palette results=%+v, want two authorized matches and no navigation cursor", body)
	}
	if len(catalog.requests) != 2 {
		t.Fatalf("palette catalog requests=%d, want both pages to fill matches", len(catalog.requests))
	}
}
