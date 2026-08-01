package webdav

import (
	"path"
	"strings"
)

func normalizePrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "/dav/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	value = path.Clean(value)
	if value != "/" {
		value += "/"
	}
	return value
}

func cleanRequestPath(value string) string {
	if value == "" {
		return "/"
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

func pathWithinPrefix(value, prefix string) bool {
	cleaned := cleanRequestPath(value)
	root := strings.TrimSuffix(normalizePrefix(prefix), "/")
	return cleaned == root || strings.HasPrefix(cleaned, root+"/")
}
