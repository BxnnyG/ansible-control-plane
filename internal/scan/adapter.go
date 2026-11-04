package scan

import (
	"context"
	"encoding/json"
	"fmt"
)

// JSONScanner adapts Scan to callers that store the result as raw JSON
// (e.g. projects.Scanner).
type JSONScanner struct {
	Limits Limits
}

// Scan scans root and returns the JSON-encoded Result.
func (s JSONScanner) Scan(ctx context.Context, root string) (json.RawMessage, error) {
	res, err := Scan(ctx, root, s.Limits)
	if err != nil {
		return nil, fmt.Errorf("scan project: %w", err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("encode scan result: %w", err)
	}
	return b, nil
}
