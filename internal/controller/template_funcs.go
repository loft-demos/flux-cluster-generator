// internal/controller/template_funcs.go
package controller

import (
	"strings"
	"text/template"
)

// TemplateFuncMap exposes helpers for the RSIP name template.
//
// Usage example in template:
//   {{ dns1123 (coalesce (label .labels "env") (projectFromNS .namespace)) }}-{{ dns1123 (coalesce (label .labels "name") .name) }}
func TemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		// lookup functions
		"label": func(m map[string]string, key string) string {
			if m == nil {
				return ""
			}
			return m[key]
		},
		"ann": func(m map[string]string, key string) string {
			if m == nil {
				return ""
			}
			return m[key]
		},

		// small utilities
		"default": func(def, v string) string {
			if strings.TrimSpace(v) == "" {
				return def
			}
			return v
		},
		"coalesce": func(vals ...string) string {
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					return v
				}
			}
			return ""
		},

		// domain helpers from your existing package
		"dns1123":       sanitizeDNS1123,
		"projectFromNS": projectFromNamespace,
	}
}
