package stack

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
)

const MaxTransferBytes = 64 << 20

func apiSizeLimit(path string) int {
	if path == "/v1/session/transfer/export" || path == "/v1/session/transfer/import" {
		return MaxTransferBytes
	}
	return MaxRequestBytes
}

func transferJSON(w http.ResponseWriter, r *http.Request, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return failure("invalid_input", "Content-Type application/json required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(apiSizeLimit(r.URL.Path))))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return failure("invalid_input", "invalid or oversized transfer JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return failure("invalid_input", "exactly one JSON value required")
	}
	return nil
}

func (s *Service) transferHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/session/invite" && r.URL.Path != "/v1/session/link" && r.URL.Path != "/v1/session/links" && !strings.HasPrefix(r.URL.Path, "/v1/session/transfer/") {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST required"))
		return true
	}
	var data any
	var err error
	switch r.URL.Path {
	case "/v1/session/links":
		var request struct{}
		if err = requestJSON(w, r, &request); err == nil {
			data, err = s.Store.ListSessionLinks(r.Context(), token)
		}
	case "/v1/session/link":
		var request SessionLinkRequest
		if err = requestJSON(w, r, &request); err == nil {
			data, err = s.Store.WriteSessionLink(r.Context(), token, request)
		}
	case "/v1/session/invite":
		var request SessionInviteRequest
		if err = requestJSON(w, r, &request); err == nil {
			data, err = s.Store.InviteSession(r.Context(), token, request)
		}
	case "/v1/session/transfer/export":
		var request TransferExportRequest
		if err = transferJSON(w, r, &request); err == nil {
			data, err = s.Store.ExportTransfer(r.Context(), token, request)
		}
	case "/v1/session/transfer/import":
		var request TransferImportRequest
		if err = transferJSON(w, r, &request); err == nil {
			data, err = s.Store.ImportTransfer(r.Context(), token, request)
		}
	case "/v1/session/transfer/status", "/v1/session/transfer/seal", "/v1/session/transfer/activate":
		var request TransferChangeRequest
		if err = requestJSON(w, r, &request); err == nil {
			data, err = s.Store.ChangeTransfer(r.Context(), token, strings.TrimPrefix(r.URL.Path, "/v1/session/transfer/"), request)
		}
	default:
		err = failure("not_found", "unknown transfer endpoint")
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
