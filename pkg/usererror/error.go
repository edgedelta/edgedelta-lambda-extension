package usererror

// Error interface is predeclared like actual error interface.
// Additional message will be lost if user Error is used with multierror or if context added with fmt.Errorf in the call chain.
type Error interface {
	Error() string
	Message() string
	SetMessage(message string)
}

// New returns a user error that formats as the given raw and friendly error messages.
func New(raw, friendly string) Error {
	return &userError{raw, friendly}
}

// Error returns raw error.
func (u *userError) Error() string {
	return u.raw
}

// Message returns friendly message.
func (u *userError) Message() string {
	return u.message
}

// SetMessage .
func (u *userError) SetMessage(message string) {
	u.message = message
}

// userError is a drop in replacement for standard error to carry additional user friendly error message.
type userError struct {
	raw     string // raw error message
	message string // user friendly error message
}
