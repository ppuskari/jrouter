param(
    [long]$RunId = 0,
    [string]$ArtifactRoot = '.\build\v1.0.0'
)

$ErrorActionPreference = 'Stop'
$branch = 'release/globaltalk-aurp-router-v1.0.0'
$tag = 'v1.0.0'

$repo = (git rev-parse --show-toplevel).Trim()
if (-not $repo) { throw 'Run this script from inside the jrouter repository.' }
Set-Location $repo
Get-Command gh -ErrorAction Stop | Out-Null

if ((git status --porcelain)) { throw 'Working tree is not clean.' }
git fetch origin --prune --tags
if ($LASTEXITCODE -ne 0) { throw 'git fetch failed.' }

git checkout $branch
if ($LASTEXITCODE -ne 0) { throw "Unable to checkout $branch" }
git merge --ff-only "origin/$branch"
if ($LASTEXITCODE -ne 0) { throw 'Release branch is not a clean fast-forward from GitHub.' }

$head = (git rev-parse HEAD).Trim()
$remote = (git rev-parse "origin/$branch").Trim()
if ($head -ne $remote) { throw 'Local release branch differs from GitHub.' }
if ((Get-Content meta/VERSION -Raw).Trim() -ne '1.0.0') { throw 'meta/VERSION is not 1.0.0.' }

& "$PSScriptRoot\Download-Release1.ps1" -RunId $RunId -Destination $ArtifactRoot
if ($LASTEXITCODE -ne 0) { throw 'Final artifact verification failed.' }

$buildInfo = @{}
Get-Content (Join-Path $ArtifactRoot 'BUILD-INFO.txt') | ForEach-Object {
    $parts = $_ -split '=', 2
    if ($parts.Count -eq 2) { $buildInfo[$parts[0]] = $parts[1] }
}
if ($buildInfo['head_sha'] -ne $head) {
    throw "Artifacts are from $($buildInfo['head_sha']), but release branch is $head."
}

$existingTag = git tag --list $tag
if ($existingTag) {
    $tagSha = (git rev-list -n 1 $tag).Trim()
    if ($tagSha -ne $head) { throw "$tag already exists at $tagSha, not $head." }
} else {
    git tag -a $tag $head -m 'GlobalTalk AURP Router 1.0.0'
    if ($LASTEXITCODE -ne 0) { throw 'Unable to create release tag.' }
    git push origin $tag
    if ($LASTEXITCODE -ne 0) { throw 'Unable to push release tag.' }
}

$linuxDir = Join-Path $ArtifactRoot 'globaltalk-aurp-router-v1.0.0-linux-amd64'
$windowsDir = Join-Path $ArtifactRoot 'globaltalk-aurp-router-v1.0.0-windows-amd64'
$assets = @(
    (Join-Path $linuxDir 'globaltalk-aurp-router-v1.0.0-linux-amd64'),
    (Join-Path $linuxDir 'globaltalk-aurp-router-v1.0.0-linux-amd64.sha256'),
    (Join-Path $windowsDir 'globaltalk-aurp-router-v1.0.0-windows-amd64.exe'),
    (Join-Path $windowsDir 'globaltalk-aurp-router-v1.0.0-windows-amd64.sha256')
)

$releaseExists = $false
try {
    gh release view $tag --repo ppuskari/jrouter *> $null
    if ($LASTEXITCODE -eq 0) { $releaseExists = $true }
} catch {}
if ($releaseExists) { throw "GitHub release $tag already exists; refusing to overwrite it." }

$notes = @'
# GlobalTalk AURP Router 1.0.0

First stable GlobalTalk AURP Router release from the RFC 1504/AURP implementation and RC soak line.

Highlights:
- RFC 1504 AURP routing and zone exchange coverage used by the 1.0 release line.
- Hard-seed, soft-seed, and non-seed EtherTalk operation.
- Split-horizon and route lifecycle hardening.
- Operator status and peering views with per-route DDP byte telemetry/top talkers.
- Linux amd64 and Windows amd64 binaries built from the same source commit.

The release binaries embed the exact Git commit SHA. Verify with `-version` and the published SHA-256 files.
'@

$notesFile = Join-Path $ArtifactRoot 'RELEASE-NOTES.md'
$notes | Set-Content -Path $notesFile -Encoding UTF8

gh release create $tag @assets --repo ppuskari/jrouter --title 'GlobalTalk AURP Router 1.0.0' --notes-file $notesFile --verify-tag
if ($LASTEXITCODE -ne 0) { throw 'GitHub release creation failed.' }

Write-Host '=== GLOBALTALK AURP ROUTER 1.0.0 PUBLISHED ==='
Write-Host "Tag:    $tag"
Write-Host "Commit: $head"
