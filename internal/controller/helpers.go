package controller

import (
	"fmt"
	"strings"
	"unicode"
)

// Best-effort derive project from namespace (supports "p-<project>")
func projectFromNamespace(ns string) string {
	if strings.HasPrefix(ns, "p-") && len(ns) > 2 {
		return ns[2:]
	}
	return ""
}

// Sanitize to DNS-1123 label (lowercase alnum and '-', max 63, no leading/trailing '-')
func sanitizeDNS1123(in string) string {
	if in == "" {
		return "id"
	}
	// Lowercase and map disallowed -> '-'
	sb := strings.Builder{}
	sb.Grow(len(in))
	for _, r := range strings.ToLower(in) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	out := strings.Trim(sb.String(), "-")
	if len(out) == 0 {
		out = "id"
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// Prefix matcher for label-copy
func hasAnyPrefix(prefixes []string, key string) bool {
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p != "" && strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// toCamel converts keys like "flux-app/podinfo", "team-name", "env.region"
// -> "fluxAppPodinfo", "teamName", "envRegion"
func toCamel(s string) string {
	if s == "" {
		return s
	}
	// split on common separators
	parts := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '-', '_', '.', '/', ':':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return s
	}
	// lower first, TitleCase the rest
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		// Title case: upper first rune, lower the rest
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		for i := 1; i < len(runes); i++ {
			runes[i] = unicode.ToLower(runes[i])
		}
		b.WriteString(string(runes))
	}
	// strip any remaining non-alnum just in case
	out := b.String()
	clean := strings.Builder{}
	for _, r := range out {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			clean.WriteRune(r)
		}
	}
	res := clean.String()
	if res == "" {
		return "key"
	}
	return res
}

// Deep compare two map[string]any (used for spec drift)
func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch at := va.(type) {
		case map[string]any:
			bt, ok := vb.(map[string]any)
			if !ok {
				return false
			}
			if !mapsEqual(at, bt) {
				return false
			}
		default:
			if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) {
				return false
			}
		}
	}
	return true
}
