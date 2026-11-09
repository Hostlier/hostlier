# Local package init hooks

`hostlier init --package NAME` selects a local executable package. The executable must be installed in the selected local package directory (normally `/var/lib/hostlier/packages/NAME`). You can invoke the initializer directly or after plain `hostlier init` and `hostlier package add NAME`. When completing a starter manifest, Hostlier preserves existing package entries and fills in the workload from the initializer. An explicit `--state-dir DIR` searches only `DIR/packages/NAME`. Install a native executable with `hostlier package install NAME --file BINARY`. Use `hostlier package describe NAME` to inspect its capabilities.

A package opts in by returning `"hooks":["init"]` in its existing v1 `describe` capability. Packages with their own project initializer can omit the field. Explicitly selecting one of those packages reports that it does not provide an init hook.

Hostlier invokes the same executable snapshot with the single argument `init`, in the selected project directory. It sends one JSON document on stdin. The following request shows all optional flags. The optional `config` object carries previously entered public settings for this package. A fresh project without flags receives only the version and suggested name.

```json
{"version":1,"defaultName":"example","name":"hello","route":"hello.example.com","architecture":"arm64","port":8080}
```

The package can inspect existing project files. Return one JSON document on stdout containing a Hostlier v2 project with an explicit source and optional files to create. Project fields use Hostlier’s JSON model. In `secrets`, use stored secret names. Hostlier serializes those as `secret://` references in the project file.

```json
{"version":1,"project":{"version":2,"name":"hello","source":{"image":"hello:1.0.0"},"endpoint":{"exposure":"public","port":8080}},"files":{"hello.txt":"Hello\n"}}
```

A hook that cannot initialize the project may return `{"version":1,"error":"Public explanation"}` with exit status zero. A nonzero exit reports a hook failure. The process has a 30-second deadline and a 4 MiB output limit. Keep credentials out of diagnostics and responses.

Hooks must only propose changes, including during a dry run. They execute locally with the user's filesystem permissions. Hostlier validates the project, reserves the `.hostlier` filename and applies new files together with overwrite protection. `--dry-run` shows the proposal. The generated `.hostlier` enrolls the selected package under `packages.NAME` with the same declared inputs and defaults as `hostlier package add NAME`. A Minion initializer also accepts `--ui` for a new React starter (`"ui":true` in the request). The controller receives the deployment spec without this local field.

For existing projects with their own initializer, select the package directly at plan or deploy time. See the [local project hook contract](package-project.md) and the [controller deployment hook](package-deploy.md).
