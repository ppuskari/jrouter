# Set28 Build and Download Helpers

Set28 is built by GitHub Actions. Windows is used for Git/PowerShell control;
no local Windows Go toolchain is required for the normal soak-build workflow.

The active branch is:

`petar/aurp-set28-rc-prep-20260901`

The CI workflow and artifact names are:

- workflow: `AURP Set28 RC Prep`
- artifact: `jrouter-set28-linux-amd64`

## Download the latest successful Set28 build

From the repository root in PowerShell 5.1:

```powershell
.\scripts\Download-Set28RC.ps1
```

The helper selects exactly one successful Set28 workflow run, downloads the
Linux artifact, verifies its SHA-256 against the checksum produced by CI, and
writes `BUILD-INFO.txt` beside the binary.

To download a specific known run:

```powershell
.\scripts\Download-Set28RC.ps1 -RunId 33621389957 -Destination .\build\set28-rcproof1
```

The explicit run ID is scalar, so PowerShell cannot accidentally expand two
run IDs into the positional arguments for `gh run download`.

## Trigger, watch, and download an exact-SHA Set28 build

When already on the Set28 branch:

```powershell
.\scripts\Invoke-Set28RC.ps1
```

With a custom commit message:

```powershell
.\scripts\Invoke-Set28RC.ps1 -Message "Set28: RC soak trigger"
```

The helper:

1. verifies the current branch;
2. fetches the remote Set28 branch and refuses to build a behind/diverged tree;
3. checks that `build/` contains no tracked files;
4. commits current source changes, or creates an empty CI-trigger commit when
   the tree is clean;
5. pushes the exact candidate SHA;
6. waits for the workflow run whose `head_sha` exactly matches that commit;
7. watches the complete CI gate;
8. prints failed logs automatically if CI fails;
9. downloads the exact successful artifact;
10. verifies its SHA-256.

## Output layout

The default download location is:

`build\set28-rc-prep`

or, for download-only mode:

`build\set28-latest`

A successful directory contains:

- `jrouter-set28-linux-amd64`
- `jrouter-set28-linux-amd64.sha256`
- `BUILD-INFO.txt`

`BUILD-INFO.txt` records the workflow, run ID, exact commit SHA, artifact
name, and verified binary SHA-256. Keep that file with any soak binary so field
results always have exact provenance.
