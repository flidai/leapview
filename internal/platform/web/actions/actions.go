package actions

import (
	"regexp"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func Get(path string, signalPaths ...string) string {
	return request("get", path, signalPaths, "")
}

// GetPathExpression issues a read-only request whose path is evaluated from
// the current event. Callers must build the expression from encoded signal
// values; it is kept separate from Get so static paths remain the default.
func GetPathExpression(pathExpression string, signalPaths ...string) string {
	return requestWithPathExpression("get", pathExpression, signalPaths, "")
}

// QueryPost is an explicitly non-mutating POST used for signal-backed search
// and read-model commands whose payload is too rich for a query string.
func QueryPost(path string, signalPaths ...string) string {
	return request("post", path, signalPaths, "")
}

// EventPost dispatches a transient UI state event. Durable application
// mutations must use CommandPost so they carry a generated operation ID.
func EventPost(path string, signalPaths ...string) string {
	return request("post", path, signalPaths, "")
}

func CommandPost(binding uicommand.Binding, path string, signalPaths ...string) string {
	return request("post", path, signalPaths, jsString(binding.OperationID()))
}

func CommandPatch(binding uicommand.Binding, path, revision string, signalPaths ...string) string {
	return requestWithHeaders("patch", path, signalPaths, "window.LeapViewCommand.headers("+jsString(binding.OperationID())+", "+jsString(revision)+")")
}

// CommandPostSwitch chooses one typed command from a closed server-shared set.
// selectorExpression is a Datastar expression such as "evt.detail.action".
func CommandPostSwitch(selectorExpression string, bindings map[string]uicommand.Binding, path string, signalPaths ...string) string {
	return commandPostSwitch(selectorExpression, bindings, path, "", signalPaths...)
}

// CommandPostSwitchWithRevision chooses a typed command and supplies a
// browser signal expression as its If-Match value.
func CommandPostSwitchWithRevision(selectorExpression string, bindings map[string]uicommand.Binding, path, ifMatchExpression string, signalPaths ...string) string {
	return commandPostSwitch(selectorExpression, bindings, path, strings.TrimSpace(ifMatchExpression), signalPaths...)
}

func commandPostSwitch(selectorExpression string, bindings map[string]uicommand.Binding, path, ifMatchExpression string, signalPaths ...string) string {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, jsString(key)+": "+jsString(bindings[key].OperationID()))
	}
	operationExpression := "({" + strings.Join(entries, ", ") + "})[" + strings.TrimSpace(selectorExpression) + "]"
	if ifMatchExpression != "" {
		return requestWithHeaders("post", path, signalPaths, "window.LeapViewCommand.headers("+operationExpression+", "+ifMatchExpression+")")
	}
	return request("post", path, signalPaths, operationExpression)
}

// CommandPostSequence declares an explicit composed UI workflow. Every
// dispatched mutation still verifies its individual generated operation ID.
func CommandPostSequence(bindings []uicommand.Binding, path string, signalPaths ...string) string {
	return request("post", path, signalPaths, operationArrayExpression(bindings))
}

func CommandPostConditional(conditionExpression string, ifTrue, ifFalse []uicommand.Binding, path string, signalPaths ...string) string {
	operationExpression := "(" + strings.TrimSpace(conditionExpression) + " ? " + operationArrayExpression(ifTrue) + " : " + operationArrayExpression(ifFalse) + ")"
	return request("post", path, signalPaths, operationExpression)
}

func operationArrayExpression(bindings []uicommand.Binding) string {
	operations := make([]string, 0, len(bindings))
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		operationID := binding.OperationID()
		if _, exists := seen[operationID]; exists {
			continue
		}
		seen[operationID] = struct{}{}
		operations = append(operations, jsString(operationID))
	}
	return "[" + strings.Join(operations, ", ") + "]"
}

func request(method, path string, signalPaths []string, operationExpression string) string {
	return requestWithPathExpression(method, jsString(path), signalPaths, operationExpression)
}

func requestWithHeaders(method, path string, signalPaths []string, headers string) string {
	return requestWithPathExpressionAndHeaders(method, jsString(path), signalPaths, headers)
}

func requestWithPathExpression(method, pathExpression string, signalPaths []string, operationExpression string) string {
	headers := "window.LeapViewCommand.headers()"
	if strings.TrimSpace(operationExpression) != "" {
		headers = "window.LeapViewCommand.headers(" + operationExpression + ")"
	}
	return requestWithPathExpressionAndHeaders(method, pathExpression, signalPaths, headers)
}

func requestWithPathExpressionAndHeaders(method, pathExpression string, signalPaths []string, headers string) string {
	options := "headers: " + headers
	if len(signalPaths) > 0 {
		patterns := make([]string, 0, len(signalPaths))
		for _, signalPath := range signalPaths {
			patterns = append(patterns, strings.ReplaceAll(regexp.QuoteMeta(signalPath), `\.`, `[.]`))
		}
		include := "/^(?:" + strings.Join(patterns, "|") + ")(?:[.]|$)/"
		options = "filterSignals: {include: " + include + "}, " + options
	}
	return "@" + method + "(" + pathExpression + ", {" + options + "})"
}

func jsSingleQuoted(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`).Replace(value)
}

func jsString(value string) string {
	return "'" + jsSingleQuoted(value) + "'"
}
