package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// httpHandler wraps the mux with recovery, timing, history and compression.
func (a *App) httpHandler() http.Handler {
	return recoverMiddleware(a, timingMiddleware(gzipMiddleware(a.historyMiddleware(a.mux))))
}

func recoverMiddleware(a *App, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				a.appendAppLog("error", "http handler panic", map[string]any{"component": "http", "path": r.URL.Path, "panic": fmt.Sprint(rec)})
				writeJSON(w, http.StatusInternalServerError, jsonResponse{OK: false, Message: "Внутренняя ошибка сервера"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type timingWriter struct {
	http.ResponseWriter
	started     time.Time
	wroteHeader bool
}

func (w *timingWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.Header().Set("Server-Timing", fmt.Sprintf("app;dur=%.1f", float64(time.Since(w.started).Microseconds())/1000))
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *timingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *timingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// timingMiddleware exposes handler time to the browser devtools (Server-Timing)
// so slow handlers can be identified without server access.
func timingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&timingWriter{ResponseWriter: w, started: time.Now()}, r)
	})
}

var gzipWriterPool = sync.Pool{New: func() any {
	zw, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return zw
}}

type gzipResponseWriter struct {
	http.ResponseWriter
	zw          *gzip.Writer
	decided     bool
	compress    bool
	wroteHeader bool
}

func compressibleContentType(ct string) bool {
	ct = strings.ToLower(ct)
	for _, prefix := range []string{"text/", "application/json", "application/javascript", "image/svg+xml", "application/xml"} {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

func (w *gzipResponseWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true
	h := w.Header()
	if h.Get("Content-Encoding") != "" || h.Get("Content-Disposition") != "" {
		return
	}
	if !compressibleContentType(h.Get("Content-Type")) {
		return
	}
	w.compress = true
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	zw := gzipWriterPool.Get().(*gzip.Writer)
	zw.Reset(w.ResponseWriter)
	w.zw = zw
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	if code == http.StatusNoContent || code == http.StatusNotModified || code < 200 {
		w.decided = true
	} else {
		w.decide()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.compress {
		return w.zw.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipResponseWriter) Flush() {
	if w.compress && w.zw != nil {
		_ = w.zw.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) close() {
	if w.compress && w.zw != nil {
		_ = w.zw.Close()
		gzipWriterPool.Put(w.zw)
		w.zw = nil
	}
}

// gzipMiddleware compresses text responses. On slow links this is the single
// largest win: HTML pages such as the task journal shrink 10–20×.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.Header.Get("Upgrade") != "" || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type staticAsset struct {
	data        []byte
	etag        string
	contentType string
}

// cachedStaticHandler serves embedded assets with strong ETags. Embedded files
// have no modification time, so without ETags every page load re-downloaded
// all CSS/JS. Versioned URLs (?v=…) are cacheable for a year.
func cachedStaticHandler(root fs.FS) http.Handler {
	assets := map[string]staticAsset{}
	_ = fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(root, p)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		assets[p] = staticAsset{data: data, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, contentType: contentTypeForPath(p, data)}
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		asset, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", asset.etag)
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300, must-revalidate")
		}
		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, asset.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", asset.contentType)
		_, _ = w.Write(asset.data)
	})
}

func contentTypeForPath(p string, data []byte) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json":
		return "application/json; charset=utf-8"
	case ".html":
		return "text/html; charset=utf-8"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}
	return http.DetectContentType(data)
}
