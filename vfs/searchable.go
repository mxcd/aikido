package vfs

import "context"

// Searchable is an OPTIONAL capability for backends that support native search.
//
// The built-in `search` tool is registered by RegisterVFSTools only when the
// supplied storage satisfies Searchable. SearchSyntax returns human-readable
// documentation of the query language; the tool description embeds it so the
// model knows what queries the backend accepts.
type Searchable interface {
	Search(ctx context.Context, query string) (paths []string, err error)
	SearchSyntax() string
}
