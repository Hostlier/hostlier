package model

// Error is the public API failure contract with a stable code and safe message.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error exposes the safe public message carried by a typed failure.
func (e *Error) Error() string { return e.Message }

// Err constructs a stable machine-readable failure and its safe public message.
func Err(code, message string) error { return &Error{Code: code, Message: message} }
