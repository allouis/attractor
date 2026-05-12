package ingest

import (
	"context"
	"time"
)

// contextWithTimeout is a small alias so the import set in server.go
// stays free of the context package directly; servers only need it for
// the shutdown call.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
