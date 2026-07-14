package api

import "testing"

func TestWebAssetNameStripsBasePath(t *testing.T) {
	tests := []struct {
		name        string
		requestPath string
		basePath    string
		want        string
	}{
		{
			name:        "base path root",
			requestPath: "/vfs-link/index",
			basePath:    "/vfs-link/index",
			want:        "index.html",
		},
		{
			name:        "asset under base path",
			requestPath: "/vfs-link/index/assets/app.js",
			basePath:    "/vfs-link/index",
			want:        "assets/app.js",
		},
		{
			name:        "spa route under base path",
			requestPath: "/vfs-link/index/share/abc",
			basePath:    "/vfs-link/index",
			want:        "share/abc",
		},
		{
			name:        "root deployment",
			requestPath: "/assets/app.js",
			basePath:    "/",
			want:        "assets/app.js",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webAssetName(tt.requestPath, cleanWebBasePath(tt.basePath)); got != tt.want {
				t.Fatalf("webAssetName() = %q, want %q", got, tt.want)
			}
		})
	}
}
