package host

import (
	"os"

	"github.com/edgedelta/edgedelta-lambda-extension/pkg/env"
)

// Name provides global variable for host name.
var Name string

func init() {
	if override := env.Get("ED_HOST_OVERRIDE"); override != "" {
		Name = override
	} else if actual, err := os.Hostname(); err == nil {
		Name = actual
	} else {
		Name = "unknown"
	}
}
