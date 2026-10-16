package config

import (
	"errors"
	"time"
)

type Health struct {
	RequireVM     bool     `yaml:"require_vm" env:"QIOTLCTL_HEALTH_REQUIRE_VM"`
	RequireHA     bool     `yaml:"require_ha" env:"QIOTLCTL_HEALTH_REQUIRE_HA"`
	CheckInterval Duration `yaml:"check_interval" env:"QIOTLCTL_HEALTH_CHECK_INTERVAL"`
	CheckTimeout  Duration `yaml:"check_timeout" env:"QIOTLCTL_HEALTH_CHECK_TIMEOUT"`
	FailureGrace  Duration `yaml:"failure_grace" env:"QIOTLCTL_HEALTH_FAILURE_GRACE"`
	MaxPendingAge Duration `yaml:"max_pending_age" env:"QIOTLCTL_HEALTH_MAX_PENDING_AGE"`
	LogInterval   Duration `yaml:"log_interval" env:"QIOTLCTL_HEALTH_LOG_INTERVAL"`
}

func DefaultHealth() Health {
	return Health{true, true, Duration(30 * time.Second), Duration(5 * time.Second), Duration(30 * time.Second), Duration(5 * time.Minute), Duration(5 * time.Minute)}
}
func (h Health) validate() error {
	if h.CheckInterval < Duration(time.Second) || h.CheckTimeout <= 0 || h.CheckTimeout > h.CheckInterval || h.FailureGrace < 0 || h.MaxPendingAge <= 0 || h.LogInterval < Duration(time.Second) {
		return errors.New("health requires check_interval/log_interval >= 1s, 0 < check_timeout <= check_interval, failure_grace >= 0 and max_pending_age > 0")
	}
	return nil
}
