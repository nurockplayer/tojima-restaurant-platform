package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

const (
	// maxTables bounds one apply so a bad file cannot rewrite a whole schema.
	maxTables    = 500
	maxNameRunes = 80
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// locatorEncoding yields 26 lowercase base32 characters for 16 random bytes.
var locatorEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// specFile is the operator input for `apply`. Unknown fields are rejected so a
// typo never provisions something the operator did not intend.
type specFile struct {
	Tenant tenantSpec  `json:"tenant"`
	Store  storeSpec   `json:"store"`
	Tables []tableSpec `json:"tables"`
}

type tenantSpec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type storeSpec struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	GuestDisplayName *string           `json:"guest_display_name"`
	QREntryMode      tablesession.Mode `json:"qr_entry_mode"`
	Cash             *cashSpec         `json:"cash"`
}

// cashSpec is optional: a file without it leaves the store's cash settings alone.
type cashSpec struct {
	Enabled bool                `json:"enabled"`
	Mode    operations.CashMode `json:"mode"`
}

// UnmarshalJSON requires both fields: an omitted "enabled" must not silently
// become false and switch cash acceptance off.
func (c *cashSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Enabled *bool               `json:"enabled"`
		Mode    operations.CashMode `json:"mode"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if raw.Enabled == nil {
		return errors.New("store.cash.enabled is required")
	}
	c.Enabled, c.Mode = *raw.Enabled, raw.Mode
	return nil
}

type tableSpec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func loadSpec(path string) (specFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return specFile{}, errors.New("provisioning file is not readable")
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var spec specFile
	if err := decoder.Decode(&spec); err != nil {
		return specFile{}, fmt.Errorf("invalid provisioning file: %s", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return specFile{}, errors.New("invalid provisioning file: trailing data")
	}
	return spec.normalize()
}

// normalize validates every field and returns trimmed names, so later steps
// compare exactly what would be written.
func (s specFile) normalize() (specFile, error) {
	var out specFile
	var err error
	if out.Tenant, err = normalizeTenant(s.Tenant); err != nil {
		return specFile{}, err
	}
	if out.Store, err = normalizeStore(s.Store); err != nil {
		return specFile{}, err
	}
	if len(s.Tables) > maxTables {
		return specFile{}, fmt.Errorf("tables: at most %d entries", maxTables)
	}
	out.Tables = make([]tableSpec, len(s.Tables))
	seen := make(map[string]bool, len(s.Tables))
	for i, table := range s.Tables {
		field := fmt.Sprintf("tables[%d].id", i)
		if err := checkID(field, table.ID); err != nil {
			return specFile{}, err
		}
		if seen[table.ID] {
			return specFile{}, fmt.Errorf("%s %q is duplicated", field, table.ID)
		}
		seen[table.ID] = true
		name, ok := cleanName(table.Name)
		if !ok {
			return specFile{}, fmt.Errorf("tables[%d].name must be 1-80 characters after trimming, without control characters", i)
		}
		out.Tables[i] = tableSpec{ID: table.ID, Name: name}
	}
	return out, nil
}

func normalizeTenant(s tenantSpec) (tenantSpec, error) {
	if err := checkID("tenant.id", s.ID); err != nil {
		return tenantSpec{}, err
	}
	name, ok := cleanName(s.Name)
	if !ok {
		return tenantSpec{}, errors.New("tenant.name must be 1-80 characters after trimming, without control characters")
	}
	return tenantSpec{ID: s.ID, Name: name}, nil
}

func normalizeStore(s storeSpec) (storeSpec, error) {
	if err := checkID("store.id", s.ID); err != nil {
		return storeSpec{}, err
	}
	name, ok := cleanName(s.Name)
	if !ok {
		return storeSpec{}, errors.New("store.name must be 1-80 characters after trimming, without control characters")
	}
	if !validMode(s.QREntryMode) {
		return storeSpec{}, errors.New("store.qr_entry_mode must be one of STAFF_OPEN, SESSION_PIN, DIRECT_SCAN")
	}
	out := storeSpec{ID: s.ID, Name: name, QREntryMode: s.QREntryMode}
	if s.GuestDisplayName != nil {
		guest, ok := cleanName(*s.GuestDisplayName)
		if !ok {
			return storeSpec{}, errors.New("store.guest_display_name must be 1-80 characters after trimming, without control characters")
		}
		out.GuestDisplayName = &guest
	}
	if s.Cash != nil {
		if s.Cash.Mode != operations.Prepaid && s.Cash.Mode != operations.PayLater {
			return storeSpec{}, errors.New("store.cash.mode must be one of PREPAID, PAY_LATER")
		}
		out.Cash = &cashSpec{Enabled: s.Cash.Enabled, Mode: s.Cash.Mode}
	}
	return out, nil
}

// checkID reports only the field and the rule: a rejected value may be
// something the operator pasted by mistake, such as a credential-bearing URL.
func checkID(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !idPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-63 lowercase letters, digits or hyphens starting with a letter or digit", field)
	}
	return nil
}

func cleanName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return "", false
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return "", false
	}
	return name, true
}

func validMode(mode tablesession.Mode) bool {
	switch mode {
	case tablesession.StaffOpen, tablesession.SessionPIN, tablesession.DirectScan:
		return true
	default:
		return false
	}
}

func newLocator() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return strings.ToLower(locatorEncoding.EncodeToString(raw[:])), nil
}

// parseOrigin accepts an https origin, or http on a loopback host for local
// pilots. A path, query or credentials would make the printed table URL wrong.
func parseOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("--public-origin must be an origin such as https://order.example.jp")
	}
	if parsed.Scheme == "http" {
		if !loopbackHost(parsed.Hostname()) {
			return "", errors.New("--public-origin must be https, or http for a loopback host")
		}
	} else if parsed.Scheme != "https" {
		return "", errors.New("--public-origin must be https, or http for a loopback host")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("--public-origin must be an origin such as https://order.example.jp")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("--public-origin must be an origin such as https://order.example.jp")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// tableURL renders the guest SPA link the QR code points at (web/src/ui/App.tsx
// reads ?table=). Without an origin the locator itself is the printable value.
func tableURL(origin, locator string) string {
	if origin == "" {
		return locator
	}
	query := url.Values{"table": {locator}}
	return origin + "/?" + query.Encode()
}
