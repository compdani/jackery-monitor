// Package jackery embeds the shared model catalog used by both backends.
package jackery

import _ "embed"

//go:embed models.json
var ModelCatalog []byte
