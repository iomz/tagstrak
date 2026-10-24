package golemu

import (
	"log/slog"
	"net/http"

	"github.com/iomz/tagstrak/v2/internal/control"
	"github.com/iomz/tagstrak/v2/internal/emulator"
)

// The only mutation is whole-scenario replacement. Validation completes before
// publication; the same endpoint can publish a one-cycle server inventory.
// Every replacement attempt is audited with revisions and the cycle count,
// never the scenario itself.
func (a *App) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		control.WriteJSON(w, http.StatusOK, a.Status())
	})
	mux.HandleFunc("PUT /scenario", func(w http.ResponseWriter, r *http.Request) {
		before := a.Status().Revision
		if !control.RequireJSON(w, r, emulator.MaxScenarioBytes) {
			control.Audit(a.logger, r, "scenario.replace", "rejected", slog.String("error_code", "unsupported_media_type"))
			return
		}
		scenario, err := emulator.DecodeScenario(r.Body)
		if err != nil {
			control.Audit(a.logger, r, "scenario.replace", "rejected", slog.String("error_code", "invalid_scenario"), slog.Uint64("revision_before", before))
			control.WriteError(w, http.StatusBadRequest, "invalid_scenario", "invalid or oversized scenario", "")
			return
		}
		if err = r.Context().Err(); err != nil {
			return
		}
		if err = a.server.Replace(scenario); err != nil {
			control.Audit(a.logger, r, "scenario.replace", "rejected", slog.String("error_code", "invalid_scenario"), slog.Uint64("revision_before", before))
			control.WriteError(w, http.StatusBadRequest, "invalid_scenario", "invalid scenario", "")
			return
		}
		control.Audit(a.logger, r, "scenario.replace", "ok",
			slog.Uint64("revision_before", before), slog.Uint64("revision_after", a.Status().Revision), slog.Int("count", scenario.Cycles()))
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
