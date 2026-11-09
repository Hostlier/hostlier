// Package selection embeds the same static manifest policy used by the TypeScript compiler.
// Node 24 strips its type-only imports, so planning works before local npm installation.
package selection

import _ "embed"

//go:embed rules.ts
var Rules string
