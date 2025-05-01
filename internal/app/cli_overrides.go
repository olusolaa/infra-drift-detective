package app

import (
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

func parseAttributesOverride(override string) map[domain.ResourceKind][]string {
	if override == "" {
		return nil
	}
	parsed := make(map[domain.ResourceKind][]string)
	pairs := strings.Split(override, ";")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}

		kind := domain.ResourceKind(strings.TrimSpace(parts[0]))
		attrsRaw := strings.Split(parts[1], ",")
		attrs := make([]string, 0, len(attrsRaw))
		for _, a := range attrsRaw {
			trimmed := strings.TrimSpace(a)
			if trimmed != "" {
				attrs = append(attrs, trimmed)
			}
		}

		if kind != "" && len(attrs) > 0 {
			parsed[kind] = attrs
		}
	}
	if len(parsed) == 0 {
		return nil
	}
	return parsed
}
