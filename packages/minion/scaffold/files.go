package scaffold

import (
	_ "embed"
	"fmt"
)

const minionStarterAction = `import { buildMinion, defineAction } from '@fused/minion';
import * as z from 'zod/mini';

export default buildMinion({
  actions: {
    greet: defineAction({
      access: 'public',
      readOnly: true,
      input: z.object({ name: z.string() }),
      output: z.string(),
      execute: ({ input }) => 'Hello, ' + input.name + '!',
    }),
  },
});
`

//go:embed ui.tsx
var minionStarterUI string

//go:embed load.ts
var minionStarterLoad string

type File struct{ Path, Content string }

// Files keeps the browser entry and its config declaration together.
func Files(name, entry string, ui bool) []File {
	config := fmt.Sprintf("{\n  \"name\": %q,\n  \"entry\": %q", name, entry)
	files := []File{{entry, minionStarterAction}}
	// The browser uses the typed client for the same action as API-only projects.
	if ui {
		config += ",\n  \"ui\": { \"entry\": \"ui.tsx\", \"load\": \"load.ts\", \"title\": \"Hello Minion\" }"
		files = append(files, File{"ui.tsx", minionStarterUI}, File{"load.ts", minionStarterLoad})
	}
	return append(files, File{"minion.json", config + "\n}\n"},
		File{"package.json", fmt.Sprintf("{\n  \"name\": %q,\n  \"private\": true,\n  \"type\": \"module\"\n}\n", name)},
		File{".gitignore", "node_modules/\nbuild/\n.minion/dev-*/\n"})
}
