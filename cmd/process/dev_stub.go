//go:build !dev

package main

import "github.com/go-chi/chi/v5"

func registerDevRoutes(_ chi.Router, _ string) {
	// No-op in production builds
}
