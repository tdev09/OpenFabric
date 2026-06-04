package thermal

// ThermalPolicy defines temperature thresholds for GPU protection.
type ThermalPolicy struct {
	// WarnCelsius: log a warning, Pulse insight shown to user.
	WarnCelsius float64
	// ThrottleCelsius: stop accepting new GPU tasks. Existing tasks continue.
	ThrottleCelsius float64
	// EmergencyCelsius: cancel all GPU tasks immediately. GPU may be damaged.
	EmergencyCelsius float64
	// RecoveryCelsius: below this temp, resume accepting tasks after throttle.
	RecoveryCelsius float64
}

// DefaultPolicy is calibrated for consumer GPUs (RTX series, RX series).
var DefaultPolicy = ThermalPolicy{
	WarnCelsius:      75.0,
	ThrottleCelsius:  85.0,
	EmergencyCelsius: 92.0,
	RecoveryCelsius:  70.0,
}

// ThermalState is the current thermal management state.
type ThermalState string

const (
	ThermalNormal    ThermalState = "normal"    // accepting all tasks
	ThermalWarning   ThermalState = "warning"   // Pulse insight shown
	ThermalThrottled ThermalState = "throttled" // new tasks rejected
	ThermalEmergency ThermalState = "emergency" // all tasks cancelled
)
