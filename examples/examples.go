// Package examples embeds the example brand packs so the binaries can offer
// them without the repository on disk.
package examples

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/brand"
)

//go:embed brand-packs/*.yaml
var packs embed.FS

// BrandPacks lists the example pack names (file names without .yaml).
func BrandPacks() []string {
	entries, _ := fs.ReadDir(packs, "brand-packs")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(names)
	return names
}

// BrandPack parses the named example pack.
func BrandPack(name string) (*brand.Pack, error) {
	data, err := packs.ReadFile(path.Join("brand-packs", name+".yaml"))
	if err != nil {
		return nil, fmt.Errorf("no example brand pack %q (have: %s)", name, strings.Join(BrandPacks(), ", "))
	}
	return brand.ParsePack(data)
}
