package config

import "testing"

func TestLoadFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		present bool
		wantErr bool
	}{
		{name: "valid", value: "127.0.0.1:8080", present: true},
		{name: "missing", wantErr: true},
		{name: "empty", value: " ", present: true, wantErr: true},
		{name: "malformed", value: "localhost", present: true, wantErr: true},
		{name: "port out of range", value: "localhost:70000", present: true, wantErr: true},
		{name: "port zero", value: "localhost:0", present: true, wantErr: true},
		{name: "signed port", value: "localhost:+80", present: true, wantErr: true},
		{name: "invalid host characters", value: "bad host:80", present: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadFromEnv(func(key string) (string, bool) { return tt.value, tt.present })
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadFromEnv() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && cfg.HTTPAddress != tt.value {
				t.Fatalf("HTTPAddress = %q, want %q", cfg.HTTPAddress, tt.value)
			}
		})
	}
}

func TestLoadFromEnvRejectsNilLookup(t *testing.T) {
	if _, err := LoadFromEnv(nil); err == nil {
		t.Fatal("LoadFromEnv(nil) error = nil, want error")
	}
}
