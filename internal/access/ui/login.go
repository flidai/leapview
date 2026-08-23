package ui

import (
	"net/url"
	"strings"

	signalcontracts "github.com/flidai/leapview/internal/access/ui/signals"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
)

type LoginPageOptions struct {
	LocalAuth          bool
	SSOAuth            bool
	MustChangePassword bool
	ProviderLabel      string
	CSRFToken          string
	Presentation       webpage.Presentation
	Assets             staticasset.Resolver
	// Error is a safe, user-facing message rendered in the branded login
	// surface. Authentication handlers must never put credential details in
	// this field (or in a URL); it is intentionally a short presentation
	// string sourced from a closed set of login outcomes.
	Error string
	// ErrorCode is the corresponding closed, non-sensitive outcome identifier.
	// It is carried into the canonical updates URL so the stream can rebuild the
	// same login presentation without placing the user-facing message in a URL.
	ErrorCode string
}

type LoginPageSignal = signalcontracts.LoginPageSignal
type StatusSignal = signalcontracts.DashboardStatus

func LoginPage(options ...LoginPageOptions) g.Node {
	opts := normalizedLoginOptions(options)
	return webpage.Render(webpage.Layout{Presentation: opts.Presentation, Assets: opts.Assets}, webpage.Spec{
		Title: opts.Presentation.ProductName + " Login", CSRFToken: opts.CSRFToken,
		Scripts:    []string{"/static/login-page.js", "/static/login-background-loader.js"},
		UpdatesURL: loginUpdatesURL(opts.ErrorCode),
		Content:    g.El("lv-login-page", g.Attr("background-module-src", opts.Assets.URL("/static/topology-background.js"))),
	})
}

func LoginBootstrapSignalsForOptions(options LoginPageOptions) map[string]any {
	opts := normalizedLoginOptions([]LoginPageOptions{options})
	return map[string]any{
		"page": LoginPageSignal{
			BackgroundModuleSrc: opts.Assets.URL("/static/topology-background.js"),
			Kind:                "login", LocalAuth: opts.LocalAuth, MustChangePassword: opts.MustChangePassword,
			ProviderLabel: opts.ProviderLabel, SSOAuth: opts.SSOAuth, Title: opts.Presentation.ProductName,
		},
		"status": StatusSignal{Error: opts.Error},
	}
}

func normalizedLoginOptions(options []LoginPageOptions) LoginPageOptions {
	opts := LoginPageOptions{
		SSOAuth: true, ProviderLabel: "Sign in with Azure Active Directory",
		Presentation: webpage.Presentation{ProductName: "LeapView", FaviconPath: "/static/favicon.svg"},
	}
	if len(options) > 0 {
		opts = options[0]
		if strings.TrimSpace(opts.ProviderLabel) == "" {
			opts.ProviderLabel = "Sign in with Azure Active Directory"
		}
		if strings.TrimSpace(opts.Presentation.ProductName) == "" {
			opts.Presentation.ProductName = "LeapView"
		}
		if strings.TrimSpace(opts.Presentation.FaviconPath) == "" {
			opts.Presentation.FaviconPath = "/static/favicon.svg"
		}
	}
	return opts
}

func loginUpdatesURL(errorCode string) string {
	values := url.Values{}
	values.Set("route", "login")
	switch strings.TrimSpace(errorCode) {
	case "invalid_credentials", "session_expired", "forbidden":
		values.Set("error", strings.TrimSpace(errorCode))
	}
	return "/updates?" + values.Encode()
}
