package http

import (
	nethttp "net/http"
	"net/url"
	"strings"
)

// Carry a conversation identity, never an arbitrary return URL. Opening that
// conversation still goes through the normal chat authorization boundary.
func builderReturnChat(r *nethttp.Request) string {
	chat := r.URL.Query().Get("returnChat")
	if chat == "" {
		ref, err := url.Parse(r.Referer())
		if err != nil || ref.Host != r.Host || (ref.Scheme != "http" && ref.Scheme != "https") || !strings.HasPrefix(ref.Path, "/chats/") {
			return ""
		}
		chat = strings.TrimPrefix(ref.Path, "/chats/")
	}
	if chat == "" || len(chat) > 256 || chat == "new" {
		return ""
	}
	for _, c := range chat {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ""
		}
	}
	return chat
}

func withBuilderReturnChat(href, chat string) string {
	if chat == "" {
		return href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	q := u.Query()
	q.Set("returnChat", chat)
	u.RawQuery = q.Encode()
	return u.String()
}
