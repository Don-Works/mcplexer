package gateway

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func buildDataItems(
	kind string, rows []json.RawMessage, docs []json.RawMessage, text string,
) ([]store.DataItem, map[string]any, string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = store.DataWorkbenchKindTable
	}
	if len(rows) == 0 && len(docs) == 0 && strings.TrimSpace(text) != "" {
		var err error
		rows, err = parseDataText(text)
		if err != nil {
			return nil, nil, "", err
		}
	}
	var items []store.DataItem
	for _, row := range rows {
		items = append(items, store.DataItem{
			Kind: store.DataWorkbenchKindTable, PayloadJSON: row, Text: dataText(row),
		})
	}
	for _, doc := range docs {
		items = append(items, store.DataItem{
			Kind: store.DataWorkbenchKindDocs, PayloadJSON: doc, Text: docText(doc),
		})
	}
	if len(items) == 0 {
		return nil, nil, "", errors.New("rows, documents, or text is required")
	}
	if len(docs) > 0 && len(rows) == 0 {
		kind = store.DataWorkbenchKindDocs
	}
	return items, inferDataSchema(rows, docs), kind, nil
}

func parseDataText(text string) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(text)
	if strings.Contains(trimmed, "\n") && strings.HasPrefix(trimmed, "{") {
		var out []json.RawMessage
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.TrimSpace(line) != "" {
				out = append(out, json.RawMessage(line))
			}
		}
		return out, nil
	}
	r := csv.NewReader(strings.NewReader(trimmed))
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return nil, errors.New("text must be CSV with a header row or JSONL")
	}
	return csvRecordsToJSON(records), nil
}

func csvRecordsToJSON(records [][]string) []json.RawMessage {
	headers := records[0]
	out := make([]json.RawMessage, 0, len(records)-1)
	for _, rec := range records[1:] {
		obj := map[string]string{}
		for i, h := range headers {
			if i < len(rec) {
				obj[h] = rec[i]
			}
		}
		b, _ := json.Marshal(obj)
		out = append(out, b)
	}
	return out
}

func inferDataSchema(rows, docs []json.RawMessage) map[string]any {
	cols := map[string]string{}
	for _, row := range rows {
		var obj map[string]any
		if json.Unmarshal(row, &obj) != nil {
			continue
		}
		for k, v := range obj {
			if _, ok := cols[k]; !ok {
				cols[k] = fmt.Sprintf("%T", v)
			}
		}
	}
	return map[string]any{"columns": cols, "rows": len(rows), "documents": len(docs)}
}

func dataText(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return string(raw)
	}
	parts := make([]string, 0, len(obj))
	for k, v := range obj {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

func docText(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return string(raw)
	}
	for _, key := range []string{"text", "content", "body", "markdown"} {
		if v, ok := obj[key].(string); ok {
			return v
		}
	}
	return dataText(raw)
}

func payloadSize(items []store.DataItem) int {
	n := 0
	for _, it := range items {
		n += len(it.PayloadJSON) + len(it.Text)
	}
	return n
}

func dataTTL(ttl *int, pinned bool) *time.Time {
	minutes := dataDefaultTTLMinutes
	if ttl != nil {
		minutes = *ttl
	}
	if minutes <= 0 || pinned && ttl == nil {
		return nil
	}
	t := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	return &t
}
