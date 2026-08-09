package api

import (
	"net/http"

	"github.com/Resinat/Resin/internal/service"
)

func HandleRouteDiagnostic(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.RouteDiagnosticRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		response, err := cp.DiagnoseRoute(r.Context(), req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, response)
	}
}
