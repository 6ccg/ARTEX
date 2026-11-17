// Package llmhttp constructs clients for every model protocol using system settings.
package llmhttp

import (
	"net/http"

	"github.com/Autumn-27/artex/egress"
)

// NewClient shares the AI-request route with model discovery and connection tests.
func NewClient() (*http.Client, error) {
	transport, err := egress.NewLLMTransport()
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: transport}, nil
}
