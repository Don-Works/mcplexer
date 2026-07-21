package brain

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

type recordIndexCache struct {
	files  map[string]store.IndexFile
	errors map[string]store.BrainError
}

func (e *Editor) loadRecordIndexCache(ctx context.Context) *recordIndexCache {
	c := &recordIndexCache{
		files:  make(map[string]store.IndexFile),
		errors: make(map[string]store.BrainError),
	}
	if e.ser == nil {
		return c
	}
	if files, err := e.store.ListIndexFiles(ctx, ""); err == nil {
		for i := range files {
			if files[i].EntityKind == "" || files[i].EntityID == "" {
				continue
			}
			c.files[recordIndexKey(files[i].EntityKind, files[i].EntityID)] = files[i]
		}
	}
	if errs, err := e.store.ListBrainErrors(ctx); err == nil {
		for i := range errs {
			if errs[i].Path == "" || errs[i].Field == "_file" {
				continue
			}
			if _, ok := c.errors[errs[i].Path]; !ok {
				if _, exists := c.errors[errs[i].Path]; !exists {
					c.errors[errs[i].Path] = errs[i]
				}
			}
		}
	}
	return c
}

func recordIndexKey(kind, id string) string {
	return kind + "\x00" + id
}

func (c *recordIndexCache) enrich(kind, id string, path, source, hash, vErr, vField *string) {
	if c == nil {
		return
	}
	f, ok := c.files[recordIndexKey(kind, id)]
	if !ok {
		return
	}
	*path = f.Path
	*hash = f.Sha
	if f.Source != "" {
		*source = f.Source
	}
	if be, ok := c.errors[f.Path]; ok {
		*vErr = be.Reason
		*vField = be.Field
	}
}
