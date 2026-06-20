package ai

import "strings"

func normalizeText(s string) string {
	s = strings.TrimSpace(s)
	s = trimEmoji(s)
	s = strings.Trim(s, "_")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "—", "-")
	s = strings.ReplaceAll(s, "…", "...")
	return s
}
