param(
    [string]$Message = "Trigger Set28 RC prep CI",
    [string]$Destination = ".\build\set28-rc-prep"
)

$ErrorActionPreference = "Stop"

$branch = "petar/aurp-set28-rc-prep-20260901"
$workflow = "AURP Set28 RC Prep"

$repo = (git rev-parse --show-toplevel).Trim()
if (-not $repo) {
    throw "Run this script from inside the jrouter repository."
}
Set-Location $repo

Get-Command gh -ErrorAction Stop | Out-Null

$current = (git branch --show-current).Trim()
if ($current -ne $branch) {
    throw "Wrong branch: $current. Expected $branch."
}

git fetch origin $branch
if ($LASTEXITCODE -ne 0) {
    throw "git fetch failed."
}

git merge-base --is-ancestor "origin/$branch" HEAD
if ($LASTEXITCODE -ne 0) {
    throw "Local branch is behind or diverged from origin/$branch. Run git pull --ff-only first."
}

$trackedBuild = @(git ls-files build)
if ($trackedBuild.Count -gt 0) {
    Write-Host "Tracked build artifacts detected:"
    $trackedBuild | ForEach-Object { Write-Host "  $_" }
    throw "build/ must not contain tracked files."
}

$dirty = @(git status --short --untracked-files=all)

if (-not $dirty) {
    Write-Host "No source changes to commit; creating an empty exact-SHA CI trigger."
    git commit --allow-empty -m $Message
} else {
    Write-Host ""
    Write-Host "=== CHANGES ==="
    git status --short --untracked-files=all

    git diff --check
    if ($LASTEXITCODE -ne 0) {
        throw "git diff --check failed."
    }

    git add -A

    git diff --cached --check
    if ($LASTEXITCODE -ne 0) {
        throw "git diff --cached --check failed."
    }

    git commit -m $Message
}

if ($LASTEXITCODE -ne 0) {
    throw "git commit failed."
}

$head = (git rev-parse HEAD).Trim()

Write-Host ""
Write-Host "Pushing exact Set28 candidate $head ..."
git push origin $branch
if ($LASTEXITCODE -ne 0) {
    throw "git push failed."
}

Write-Host ""
Write-Host "Waiting for '$workflow' run for $head ..."

$run = $null
for ($i = 0; $i -lt 60; $i++) {
    Start-Sleep -Seconds 2

    $api = gh api --method GET repos/ppuskari/jrouter/actions/runs -f branch=$branch -f per_page=30 | ConvertFrom-Json
    $run = $api.workflow_runs |
        Where-Object {
            $_.head_sha -eq $head -and
            $_.name -eq $workflow
        } |
        Select-Object -First 1

    if ($run) {
        break
    }
}

if (-not $run) {
    throw "No '$workflow' run appeared for exact SHA $head."
}

$runId = [string]$run.id

Write-Host ""
Write-Host "Run ID: $runId"
Write-Host ""

gh run watch $runId --exit-status
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "=== FAILED LOG ==="
    gh run view $runId --log-failed
    throw "Set28 RC gate failed for $head."
}

$downloadScript = Join-Path $PSScriptRoot "Download-Set28RC.ps1"
& $downloadScript -RunId ([long]$runId) -Destination $Destination
if ($LASTEXITCODE -ne 0) {
    throw "Set28 artifact download helper failed."
}

Write-Host ""
Write-Host "=== SET28 EXACT-SHA BUILD COMPLETE ==="
Write-Host "Commit: $head"
Write-Host "Run ID: $runId"
