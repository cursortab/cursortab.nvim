package ctx_test

import (
	"testing"

	"cursortab/assert"
	"cursortab/ctx"
)

type externalMaterial struct{}

func (externalMaterial) SourceID() ctx.ContextSourceID { return ctx.SourceUserActions }

func TestContextMaterialIsSealedOutsideCtxPackage(t *testing.T) {
	_, ok := any(externalMaterial{}).(ctx.ContextMaterial)
	assert.False(t, ok, "external material cannot satisfy sealed ContextMaterial")
}
