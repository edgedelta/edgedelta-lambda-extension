package log

import (
	"context"

	"go.uber.org/zap"
)

var transformFunc = nilTransformer

func nilTransformer(ctx context.Context) []Field { return []Field{} }

// Field is key/value pair to be used in context logging
type Field struct {
	Key   string
	Value string
}

// SetContextTransformFunc is used for picking converting context object into set of fields to enrich log.
func SetContextTransformFunc(t func(ctx context.Context) []Field) {
	transformFunc = t
}

func toZap(fields []Field) []zap.Field {
	result := []zap.Field{}
	for _, f := range fields {
		result = append(result, zap.String(f.Key, f.Value))
	}
	return result
}
