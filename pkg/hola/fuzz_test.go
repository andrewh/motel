package hola

import (
	"testing"

	"pgregory.net/rapid"
)

func FuzzLayout(f *testing.F) {
	f.Fuzz(rapid.MakeFuzz(propLayout))
}
