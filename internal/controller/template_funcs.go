// internal/controller/template_funcs.go
package controller

import (
	"text/template"
)

// TemplateFuncMap returns the functions available inside --rsip-name-template.
func TemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		// label "key" -> value or ""
		"label": func(key string, labels map[string]string) string {
			if labels == nil {
				return ""
			}
			return labels[key]
		},
		// ann "key" -> value or ""
		"ann": func(key string, anns map[string]string) string {
			if anns == nil {
				return ""
			}
			return anns[key]
		},
		// default "fallback" "value" -> value if non-empty, else fallback
		"default": func(fallback, val string) string {
			if val == "" {
				return fallback
			}
			return val
		},
		// coalesce a,b,c -> first non-empty
		"coalesce": func(vals ...string) string {
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},
		// dns1123 "string"
		"dns1123": sanitizeDNS1123,
		// projectFromNS "namespace"
		"projectFromNS": projectFromNamespace,
	}
}
