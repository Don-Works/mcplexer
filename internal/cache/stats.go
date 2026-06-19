package cache

// Stats holds cache performance metrics.
type Stats struct {
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Evictions int64   `json:"evictions"`
	Entries   int     `json:"entries"`
	HitRate   float64 `json:"hit_rate"`
}

// LayerStats aggregates stats from all cache layers for the API.
type LayerStats struct {
	ToolCall        Stats `json:"tool_call"`
	RouteResolution Stats `json:"route_resolution"`
	ToolsList       Stats `json:"tools_list"`
}
