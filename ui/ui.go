package ui

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/openfabric/openfabric/internal/api"
)

//go:embed dist/*
var embedFS embed.FS

func init() {
	subFS, err := fs.Sub(embedFS, "dist")
	if err == nil {
		api.UIFiles = http.FS(subFS)
	}
}
