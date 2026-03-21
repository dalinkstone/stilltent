package models

// ConfigError represents a configuration validation error
type ConfigError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ConfigError) Error() string {
	return e.Message
}

// ValidationError represents validation errors for a config
type ValidationError struct {
	Errors []ConfigError `json:"errors"`
}

func (e *ValidationError) Error() string {
	if len(e.Errors) == 0 {
		return "validation error"
	}
	return e.Errors[0].Error()
}
