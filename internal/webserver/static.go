package webserver

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var staticFiles embed.FS

func (s *Server) staticHandler() http.Handler {
	dist, err := fs.Sub(staticFiles, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != "/" {
			if _, err := fs.Stat(dist, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
