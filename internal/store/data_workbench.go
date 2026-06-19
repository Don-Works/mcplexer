package store

import (
	"context"
	"encoding/json"
	"time"
)

const (
	DataWorkbenchKindTable = "table"
	DataWorkbenchKindDocs  = "docs"
)

type DataWorkbenchStore interface {
	IngestDataCollection(ctx context.Context, c *DataCollection, items []DataItem) error
	GetDataCollection(ctx context.Context, workspaceID, name string) (*DataCollection, error)
	ListDataCollections(ctx context.Context, f DataCollectionFilter) ([]DataCollection, error)
	DropDataCollection(ctx context.Context, workspaceID, name string) error
	QueryDataCollection(ctx context.Context, q DataQuery) ([]map[string]any, error)
	SearchDataCollection(ctx context.Context, s DataSearch) ([]DataHit, error)
	PruneExpiredDataCollections(ctx context.Context, now time.Time) (int, error)
}

type DataCollection struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspace_id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	TagsJSON        json.RawMessage `json:"tags_json,omitempty"`
	SchemaJSON      json.RawMessage `json:"schema_json,omitempty"`
	MetadataJSON    json.RawMessage `json:"metadata_json,omitempty"`
	RowCount        int             `json:"row_count"`
	DocCount        int             `json:"doc_count"`
	Pinned          bool            `json:"pinned"`
	TTLExpiresAt    *time.Time      `json:"ttl_expires_at,omitempty"`
	SourceSessionID string          `json:"source_session_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	DeletedAt       *time.Time      `json:"deleted_at,omitempty"`
}

type DataItem struct {
	ID           string          `json:"id"`
	CollectionID string          `json:"collection_id"`
	Ordinal      int             `json:"ordinal"`
	Kind         string          `json:"kind"`
	PayloadJSON  json.RawMessage `json:"payload_json"`
	Text         string          `json:"text"`
	CreatedAt    time.Time       `json:"created_at"`
}

type DataCollectionFilter struct {
	WorkspaceID    string
	Tags           []string
	IncludeExpired bool
	IncludeDeleted bool
	Limit          int
	Offset         int
}

type DataQuery struct {
	WorkspaceID string
	Name        string
	SQL         string
	Limit       int
}

type DataSearch struct {
	WorkspaceID string
	Name        string
	Query       string
	Limit       int
}

type DataHit struct {
	ID      string          `json:"id"`
	Ordinal int             `json:"ordinal"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	Text    string          `json:"text"`
	Score   float64         `json:"score"`
}
