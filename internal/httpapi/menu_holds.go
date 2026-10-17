package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type companyProductEmergencyHoldRequest struct {
	Active          *bool  `json:"active"`
	Reason          string `json:"reason"`
	ExpectedVersion *int64 `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

func (a MenuAPI) getCompanyProductEmergencyHold(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := a.authorizeCompany(w, r, "menu.emergency_hold.read")
	if !ok {
		return
	}
	hold, err := a.Catalog.GetCompanyProductEmergencyHold(r.Context(), tenantID, r.PathValue("productKey"))
	respondMenu(w, hold, err)
}

func (a MenuAPI) setCompanyProductEmergencyHold(w http.ResponseWriter, r *http.Request) {
	tenantID, actor, ok := a.authorizeCompany(w, r, "menu.emergency_hold.write")
	if !ok {
		return
	}
	var input companyProductEmergencyHoldRequest
	if err := decodeCompanyProductEmergencyHoldRequest(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if input.Active == nil || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 || strings.TrimSpace(input.Reason) == "" || strings.TrimSpace(input.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	hold, err := a.Catalog.SetCompanyProductEmergencyHold(
		r.Context(), tenantID, r.PathValue("productKey"), actor.SubjectID,
		*input.Active, input.Reason, *input.ExpectedVersion, input.IdempotencyKey,
	)
	respondMenu(w, hold, err)
}

func decodeCompanyProductEmergencyHoldRequest(w http.ResponseWriter, r *http.Request, input *companyProductEmergencyHoldRequest) error {
	const limit = 16 << 10
	if r.ContentLength > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return err
	}
	if len(body) > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return decodeJSON(w, r, input)
}

func validateUniqueJSONKeys(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return errInvalidHoldJSON
	}
	seen := make(map[string]struct{})
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errInvalidHoldJSON
		}
		switch key {
		case "active", "reason", "expected_version", "idempotency_key":
		default:
			return errInvalidHoldJSON
		}
		if _, exists := seen[key]; exists {
			return errInvalidHoldJSON
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return err
		}
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return errInvalidHoldJSON
	}
	if _, err = d.Token(); err != io.EOF {
		return errInvalidHoldJSON
	}
	return nil
}

var errInvalidHoldJSON = errors.New("invalid emergency hold request JSON")
