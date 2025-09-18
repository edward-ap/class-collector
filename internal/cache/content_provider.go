package cache

// ContentProvider provides access to file contents for similarity pass.
// old=true  -> read from old snapshot root (Removed)
// old=false -> read from current tree (Added)
// If not set (nil), similarity pass is skipped.

type ContentProvider interface {
	Read(path string, old bool) ([]byte, error)
}

var contentProvider ContentProvider

// SetContentProvider sets global provider for delta similarity pass.
func SetContentProvider(p ContentProvider) { contentProvider = p }

func getProvider() ContentProvider { return contentProvider }
