package provider

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/qunqin24/polyglot/internal/protocol"
)

// RebaseVendor points a built-in vendor at another origin, keeping every path,
// so a test can exercise the real URL layout without reaching the internet.
// The returned function restores the original. Tests only.
func RebaseVendor(id, origin string) (restore func(), err error) {
	v, ok := vendors[id]
	if !ok {
		return nil, fmt.Errorf("no vendor %q", id)
	}
	to, err := url.Parse(origin)
	if err != nil {
		return nil, err
	}
	rebase := func(raw string) string {
		u, _ := url.Parse(raw)
		u.Scheme, u.Host = to.Scheme, to.Host
		return u.String()
	}
	saved := *v
	bases := make(map[protocol.Name]string, len(v.Bases))
	for p, b := range v.Bases {
		bases[p] = rebase(b)
	}
	v.Bases, v.ModelsURL, v.Root = bases, rebase(v.ModelsURL), strings.TrimRight(rebase(v.Root), "/")
	return func() { *v = saved }, nil
}
