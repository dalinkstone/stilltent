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

// AddError adds a new validation error
func (e *ValidationError) AddError(field, message string) {
	e.Errors = append(e.Errors, ConfigError{Field: field, Message: message})
}

// HasErrors returns true if there are validation errors
func (e *ValidationError) HasErrors() bool {
	return len(e.Errors) > 0
}
