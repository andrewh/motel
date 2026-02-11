// Package semconv provides embedded OTel semantic convention model files.
// The YAML files are vendored from the opentelemetry/semantic-conventions repository.
package semconv

import "embed"

//go:embed model
var ModelFS embed.FS
