param(
    [switch]$ResetToReleaseBranch
)

$ErrorActionPreference = 'Stop'
$branch = 'release/globaltalk-aurp-router-v1.0.0'
$remote = 'origin'

$repo = (git rev-parse --show-toplevel).Trim()
if (-not $repo) { throw 'Run this script from inside the jrouter repository.' }
Set-Location $repo

if ((git status --porcelain)) {
    throw 'Working tree is not clean. Commit or stash local changes first.'
}

git fetch $remote --prune --tags
if ($LASTEXITCODE -ne 0) { throw 'git fetch failed.' }

if ($ResetToReleaseBranch) {
    git checkout -B $branch "$remote/$branch"
} else {
    $exists = git branch --list $branch
    if ($exists) {
        git checkout $branch
        git merge --ff-only "$remote/$branch"
    } else {
        git checkout -b $branch --track "$remote/$branch"
    }
}
if ($LASTEXITCODE -ne 0) { throw 'Unable to reconcile the local release branch.' }

$local = (git rev-parse HEAD).Trim()
$remoteSha = (git rev-parse "$remote/$branch").Trim()
if ($local -ne $remoteSha) { throw "Local HEAD $local does not match $remoteSha" }

$version = (Get-Content meta/VERSION -Raw).Trim()
if ($version -ne '1.0.0') { throw "Unexpected version '$version'." }

Write-Host '=== GLOBALTALK AURP ROUTER 1.0 RELEASE TREE READY ==='
Write-Host "Branch:  $branch"
Write-Host "Commit:  $local"
Write-Host "Version: $version"
Write-Host 'Working tree is clean and exactly matches GitHub.'
