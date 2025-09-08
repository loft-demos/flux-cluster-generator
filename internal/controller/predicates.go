package controller

import (
	"log"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)

func ParseSelectorOrDie(s string) labels.Selector {
	if s == "" {
		return labels.Everything()
	}
	parsed, err := labels.Parse(s)
	if err != nil {
		log.Fatalf("invalid selector %q: %v", s, err)
	}
	return parsed
}

func SplitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	ps := strings.Split(csv, ",")
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
