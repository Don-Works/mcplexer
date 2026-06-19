package compact

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CompactArray converts an array of homogeneous objects to columnar format.
// For arrays <3 items or heterogeneous structures, returns pruned items as-is.
func CompactArray(items []map[string]any) any {
	if len(items) < 3 {
		return pruneToSlice(items)
	}
	if !isHomogeneous(items) {
		return pruneToSlice(items)
	}

	pruned := make([]map[string]any, len(items))
	for i, item := range items {
		pruned[i] = PruneObject(item)
	}

	keyCount := make(map[string]int)
	for _, item := range pruned {
		for k := range item {
			keyCount[k]++
		}
	}

	fixed := findFixedColumns(pruned, keyCount)
	cols := buildColumnList(keyCount, fixed)

	rows := make([]any, len(pruned))
	for i, item := range pruned {
		row := make([]any, len(cols))
		for j, col := range cols {
			row[j] = item[col]
		}
		rows[i] = row
	}

	colsAny := make([]any, len(cols))
	for i, c := range cols {
		colsAny[i] = c
	}

	result := map[string]any{
		"_cols": colsAny,
		"_rows": rows,
	}
	if len(fixed) > 0 {
		result["_fixed"] = fixed
	}
	return result
}

func pruneToSlice(items []map[string]any) []any {
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = PruneObject(item)
	}
	return result
}

func isHomogeneous(items []map[string]any) bool {
	allKeys := make(map[string]bool)
	for _, item := range items {
		for k := range item {
			allKeys[k] = true
		}
	}
	if len(allKeys) == 0 {
		return false
	}

	keyCounts := make(map[string]int)
	for _, item := range items {
		for k := range item {
			keyCounts[k]++
		}
	}

	threshold := max(len(items)*80/100, 1)
	common := 0
	for _, count := range keyCounts {
		if count >= threshold {
			common++
		}
	}
	return common >= len(allKeys)/2
}

func findFixedColumns(
	items []map[string]any, keyCount map[string]int,
) map[string]any {
	fixed := make(map[string]any)
	n := len(items)
	for k, count := range keyCount {
		if count != n {
			continue
		}
		ref := items[0][k]
		if ref == nil {
			continue
		}
		allSame := true
		refJSON, _ := json.Marshal(ref)
		for _, item := range items[1:] {
			curJSON, _ := json.Marshal(item[k])
			if string(curJSON) != string(refJSON) {
				allSame = false
				break
			}
		}
		if allSame {
			fixed[k] = ref
		}
	}
	return fixed
}

func buildColumnList(
	keyCount map[string]int, fixed map[string]any,
) []string {
	cols := make([]string, 0, len(keyCount))
	for k := range keyCount {
		if _, isFixed := fixed[k]; !isFixed {
			cols = append(cols, k)
		}
	}
	sortColumns(cols)
	return cols
}

func sortColumns(cols []string) {
	priority := map[string]int{"id": 0, "name": 1, "title": 2}
	sort.Slice(cols, func(i, j int) bool {
		pi, oki := priority[cols[i]]
		pj, okj := priority[cols[j]]
		if oki && okj {
			return pi < pj
		}
		if oki {
			return true
		}
		if okj {
			return false
		}
		return cols[i] < cols[j]
	})
}

// FormatColumnar renders a columnar result as a pipe-delimited table.
// Returns "" if data is not in columnar format.
func FormatColumnar(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	colsRaw, hasCols := m["_cols"]
	rowsRaw, hasRows := m["_rows"]
	if !hasCols || !hasRows {
		return ""
	}

	cols := extractCols(colsRaw)
	if cols == nil {
		return ""
	}
	rows := extractRows(rowsRaw, len(cols))
	if rows == nil {
		return ""
	}
	return renderTable(cols, rows, m["_fixed"])
}

func extractCols(raw any) []string {
	switch c := raw.(type) {
	case []string:
		return c
	case []any:
		cols := make([]string, len(c))
		for i, v := range c {
			cols[i] = fmt.Sprintf("%v", v)
		}
		return cols
	default:
		return nil
	}
}

func extractRows(raw any, numCols int) [][]string {
	slice, ok := raw.([]any)
	if !ok {
		return nil
	}
	rows := make([][]string, len(slice))
	for i, item := range slice {
		cells := make([]string, numCols)
		if row, ok := item.([]any); ok {
			for j := 0; j < numCols && j < len(row); j++ {
				cells[j] = formatCell(row[j])
			}
		}
		rows[i] = cells
	}
	return rows
}

func renderTable(
	cols []string, rows [][]string, fixedRaw any,
) string {
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = len(c)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var sb strings.Builder

	if fixed := formatFixed(fixedRaw); fixed != "" {
		sb.WriteString(fixed)
		sb.WriteByte('\n')
	}

	for i, c := range cols {
		if i > 0 {
			sb.WriteString(" | ")
		}
		sb.WriteString(padRight(c, widths[i]))
	}
	sb.WriteByte('\n')

	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				sb.WriteString(" | ")
			}
			if i < len(widths) {
				sb.WriteString(padRight(cell, widths[i]))
			}
		}
		sb.WriteByte('\n')
	}

	return strings.TrimRight(sb.String(), "\n")
}

func formatFixed(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, formatCell(v)))
	}
	sort.Strings(parts)
	return fmt.Sprintf("[all: %s]", strings.Join(parts, ", "))
}

func formatCell(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]any, []any:
		data, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(data)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
