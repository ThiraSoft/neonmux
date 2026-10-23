package remote

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThiraSoft/neonmux/internal/gfx"
)

//go:embed web
var webFiles embed.FS

const pageCSP = "default-src 'self'; script-src 'self'; connect-src 'self' https:; img-src 'self' blob: data:; style-src 'self'; font-src 'self'; media-src 'self' blob:; frame-ancestors 'none'"

var pageFonts = map[string]int{
	"/fonts/IosevkaTerm-Regular.ttf":    0,
	"/fonts/IosevkaTerm-Bold.ttf":       gfx.StyleBold,
	"/fonts/IosevkaTerm-Italic.ttf":     gfx.StyleItalic,
	"/fonts/IosevkaTerm-BoldItalic.ttf": gfx.StyleBold | gfx.StyleItalic,
}

// pageHandler serves the PWA to tailscale serve: static files only.
func pageHandler(version string) http.Handler {
	sub, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	versionJSON, _ := json.Marshal(struct {
		Version string `json:"version"`
		Proto   int    `json:"proto"`
	}{version, wireProto})

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", pageCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := req.URL.Path
		if style, ok := pageFonts[p]; ok {
			h.Set("Content-Type", "font/ttf")
			http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(gfx.IosevkaTTF(style)))
			return
		}
		switch p {
		case "/version.json":
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-cache")
			_, _ = w.Write(versionJSON)
			return
		case "/", "/index.html", "/sw.js":
			h.Set("Cache-Control", "no-cache")
		}
		// No directory listings.
		if p != "/" && strings.HasSuffix(p, "/") {
			http.NotFound(w, req)
			return
		}
		files.ServeHTTP(w, req)
	})
}

// startPage listens on 127.0.0.1 only and serves the PWA until closed.
func startPage(port int, version string) (*http.Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           pageHandler(version),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
