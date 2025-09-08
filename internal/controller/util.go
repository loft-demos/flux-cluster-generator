// internal/controller/util.go
package controller

import (
	"fmt"
	"strings"
)

func boolPtr(b bool) *bool { return &b }

func sanitizeDNS1123(in string) string {
	s := strings.ToLower(in)
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}
	s = strings.Map(repl, s)
	s = strings.Trim(s, "-")
	if len(s) == 0 {
		return "id"
	}
	if len(s) > 63 {
		return s[:63]
	}
	return s
}

func hasAnyPrefix(prefixes []string, key string) bool {
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p != "" && strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func projectFromNamespace(ns string) string {
	if strings.HasPrefix(ns, "p-") && len(ns) > 2 {
		return ns[2:]
	}
	return ""
}

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

// toCamel converts keys like "flux-app/podinfo", "team-name" -> "fluxAppPodinfo", "teamName"
func toCamel(s string) string {
	if s == "" {
		return s
	}
	sep := func(r rune) bool {
		switch r {
		case '-', '_', '.', '/', ':':
			return true
		default:
			return false
		}
	}
	parts := strings.FieldsFunc(s, sep)
	if len(parts) == 0 {
		return s
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	out := strings.ToLower(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
	}
	// strip non-alnum
	var b strings.Builder
	for _, r := range out {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}
