package db

import (
	"strings"

	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/logicpath"
)

func cleanLogicPath(value string) string { return logicpath.Clean(value) }

func parseLogicPath(value string) (string, error) { return logicpath.Parse(value) }

func parseLogicPrefix(value string) (string, error) {
	trailing := strings.HasSuffix(strings.TrimSpace(value), "/")
	parsed, err := logicpath.Parse(strings.TrimSuffix(strings.TrimSpace(value), "/"))
	if err != nil {
		return "", err
	}
	if trailing && parsed != "" {
		parsed += "/"
	}
	return parsed, nil
}

func parentLogicPath(value string) string { return logicpath.Parent(value) }

func joinLogicPath(base, name string) string { return logicpath.Join(base, name) }
