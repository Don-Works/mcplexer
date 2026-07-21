package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

type usageSnapshotter interface {
	Snapshot(
		context.Context,
		[]store.SourceConfig,
		int,
		bool,
	) (store.UsageSnapshot, error)
}

type usageHandler struct {
	svc      usageSnapshotter
	settings *config.SettingsService
}

func (h *usageHandler) get(w http.ResponseWriter, r *http.Request) {
	h.snapshot(w, r, false)
}

func (h *usageHandler) refresh(w http.ResponseWriter, r *http.Request) {
	h.snapshot(w, r, true)
}

func (h *usageHandler) snapshot(
	w http.ResponseWriter,
	r *http.Request,
	force bool,
) {
	days, err := usageDays(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	configs := []store.SourceConfig{}
	if h.settings != nil {
		configs = config.UsageSourceConfigs(h.settings.Load(r.Context()))
	}
	snapshot, err := h.svc.Snapshot(r.Context(), configs, days, force)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "usage snapshot failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func usageDays(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("days")
	if raw == "" {
		return 30, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 365 {
		return 0, &usageDaysError{}
	}
	return days, nil
}

type usageDaysError struct{}

func (*usageDaysError) Error() string {
	return "days must be an integer between 1 and 365"
}
