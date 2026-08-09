package api

import (
	"net/http"

	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/service"
)

func HandleListRoutingRules(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := cp.ListRoutingRules()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		pagination, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, rules, pagination)
	}
}

func HandleCreateRoutingRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request service.RoutingRuleRequest
		if err := DecodeBody(r, &request); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		rule, err := cp.CreateRoutingRule(request)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, rule)
	}
}

func HandleGetRoutingRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "routing_rule_id")
		if !ok {
			return
		}
		rule, err := cp.GetRoutingRule(id)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, rule)
	}
}

func HandleUpdateRoutingRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "routing_rule_id")
		if !ok {
			return
		}
		var request service.UpdateRoutingRuleRequest
		if err := DecodeBody(r, &request); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		rule, err := cp.UpdateRoutingRule(id, request)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, rule)
	}
}

func HandleDeleteRoutingRule(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := requireUUIDPathParam(w, r, "id", "routing_rule_id")
		if !ok {
			return
		}
		if err := cp.DeleteRoutingRule(id); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func HandleReorderRoutingRules(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			RuleIDs []string `json:"rule_ids"`
		}
		if err := DecodeBody(r, &request); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if err := cp.ReorderRoutingRules(request.RuleIDs); err != nil {
			writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func HandlePreviewRoutingDecision(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			PlatformName string `json:"platform_name"`
			Target       string `json:"target"`
			Network      string `json:"network"`
		}
		if err := DecodeBody(r, &request); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		decision, err := cp.PreviewRoutingDecision(routing.DecisionInput{
			PlatformName: request.PlatformName, Target: request.Target, RequiredNetwork: request.Network,
		})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, decision)
	}
}
