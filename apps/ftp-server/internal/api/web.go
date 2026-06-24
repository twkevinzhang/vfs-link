package api

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func newWebHandler(root string, basePath string) http.Handler {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}

	basePath = cleanWebBasePath(basePath)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		name := webAssetName(r.URL.Path, basePath)
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if info, err := os.Stat(fullPath); err != nil || info.IsDir() {
			fullPath = filepath.Join(root, "index.html")
		}

		http.ServeFile(w, r, fullPath)
	})
}

func webAssetName(requestPath string, basePath string) string {
	assetPath := requestPath
	if basePath != "/" {
		if assetPath == basePath {
			assetPath = "/"
		} else if strings.HasPrefix(assetPath, basePath+"/") {
			assetPath = strings.TrimPrefix(assetPath, basePath)
		}
	}

	assetPath = path.Clean("/" + strings.TrimPrefix(assetPath, "/"))
	if assetPath == "/" {
		return "index.html"
	}
	return strings.TrimPrefix(assetPath, "/")
}

func cleanWebBasePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "/"
	}
	value = "/" + strings.Trim(value, "/")
	return path.Clean(value)
}
