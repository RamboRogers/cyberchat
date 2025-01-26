package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// Client represents the web client
type Client struct {
	fs http.FileSystem
}

// New creates a new web client instance
func New() (*Client, error) {
	fsys, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, err
	}
	return &Client{
		fs: http.FS(fsys),
	}, nil
}

// ServeHTTP implements http.Handler
func (c *Client) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.FileServer(c.fs).ServeHTTP(w, r)
}

// GetFileSystem returns an http.FileSystem for serving static files
func GetFileSystem() http.FileSystem {
	fsys, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}
