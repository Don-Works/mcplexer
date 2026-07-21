package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// searchQueryValues accepts the canonical string-array shape as well as the
// singular string/array shape models commonly use for search APIs. Unlike
// flexStrings, a comma inside one natural-language query is not a separator.
type searchQueryValues []string

func (v *searchQueryValues) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*v = nil
		return nil
	}
	if raw[0] == '[' {
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		*v = trimEach(values)
		return nil
	}
	if raw[0] == '{' {
		var nested struct {
			Queries searchQueryValues `json:"queries"`
			Query   searchQueryValues `json:"query"`
		}
		if err := json.Unmarshal(raw, &nested); err != nil {
			return fmt.Errorf("must contain query or queries: %w", err)
		}
		*v = nested.Queries
		if len(*v) == 0 {
			*v = nested.Query
		}
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("must be a string or array of strings: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*v = nil
	} else {
		*v = []string{value}
	}
	return nil
}

type searchToolsArgs struct {
	Queries    searchQueryValues `json:"queries"`
	Query      searchQueryValues `json:"query"`
	Q          searchQueryValues `json:"q"`
	Limit      int               `json:"limit"`
	MaxResults int               `json:"max_results"`
	Detail     string            `json:"detail"`
	Namespaces searchQueryValues `json:"namespaces"`
	Namespace  searchQueryValues `json:"namespace"`
	Tool       string            `json:"tool"`
}

func decodeSearchToolsArgs(raw json.RawMessage) (searchToolsArgs, error) {
	var args searchToolsArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return searchToolsArgs{}, err
		}
	}
	if len(args.Queries) == 0 {
		args.Queries = args.Query
	}
	if len(args.Queries) == 0 {
		args.Queries = args.Q
	}
	if args.Limit == 0 {
		args.Limit = args.MaxResults
	}
	if len(args.Namespaces) == 0 {
		args.Namespaces = args.Namespace
	}
	return args, nil
}
