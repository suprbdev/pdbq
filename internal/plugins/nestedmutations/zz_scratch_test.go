package nestedmutations_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
)

func TestScratch(t *testing.T) {
	reg, built := setupWith(t, plugin.NewRegistry(nestedmutations.New(nestedmutations.DefaultSettings())))
	for _, h := range strings.Split(os.Getenv("H"), ",") {
		if h != "" {
			fmt.Println(sdlBlock(built.SDL, h))
		}
	}
	if q := os.Getenv("Q"); q != "" {
		fmt.Println(compileRoot(t, reg, built, q).SQL)
	}
}
