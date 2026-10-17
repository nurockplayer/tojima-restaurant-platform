// Package config loads and validates runtime configuration.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Config contains non-secret process configuration.
type Config struct {
	HTTPAddress string
}

// LoadFromEnv reads APP_ADDR from lookup and rejects missing or malformed values.
func LoadFromEnv(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, fmt.Errorf("environment lookup is required")
	}
	address, ok := lookup("APP_ADDR")
	if !ok || strings.TrimSpace(address) == "" {
		return Config{}, fmt.Errorf("APP_ADDR is required")
	}
	if err := validateAddress(address); err != nil {
		return Config{}, fmt.Errorf("APP_ADDR: %w", err)
	}
	return Config{HTTPAddress: address}, nil
}

func validateAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("must be a host:port address: %w", err)
	}
	if strings.ContainsAny(host, " \t\r\n/") {
		return fmt.Errorf("host contains invalid whitespace or path characters")
	}
	for _, digit := range portText {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("port must contain decimal digits only")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
