package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/Resinat/Resin/internal/service"
)

func HandleListEgressIPs(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, err := cp.ListEgressIPs()
		if err != nil {
			writeServiceError(w, err)
			return
		}
		keyword := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("keyword")))
		if keyword != "" {
			filtered := make([]service.EgressIPResponse, 0, len(items))
			for _, item := range items {
				if strings.Contains(strings.ToLower(item.EgressIP), keyword) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		pg, ok := parsePaginationOrWriteInvalid(w, r)
		if !ok {
			return
		}
		WritePage(w, http.StatusOK, items, pg)
	}
}

func HandleUpdateEgressIPPolicy(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawIP, err := url.PathUnescape(PathParam(r, "ip"))
		if err != nil {
			writeInvalidArgument(w, "egress_ip: invalid path encoding")
			return
		}
		var req service.UpdateEgressIPPolicyRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		response, err := cp.UpdateEgressIPPolicy(rawIP, req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, response)
	}
}

func HandleBatchDrainEgressIPs(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req service.BatchDrainEgressIPsRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		if len(req.EgressIPs) == 0 {
			writeInvalidArgument(w, "egress_ips: must not be empty")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"items": cp.BatchDrainEgressIPs(req),
		})
	}
}

func HandleRotateEgressIPLeases(cp *service.ControlPlaneService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rawIP, err := url.PathUnescape(PathParam(r, "ip"))
		if err != nil {
			writeInvalidArgument(w, "egress_ip: invalid path encoding")
			return
		}
		var req service.RotateEgressIPLeasesRequest
		if err := DecodeBody(r, &req); err != nil {
			writeDecodeBodyError(w, err)
			return
		}
		results, err := cp.RotateEgressIPLeases(rawIP, req)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": results})
	}
}
