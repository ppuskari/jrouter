param(
    [string]$Message = "Trigger jrouter v1.0.0-rc1 CI",
    [string]$Destination = ".\build\v1.0.0-rc1"
)

$ErrorActionPreference = "Stop"

$branch = "petar/aurp-v1.0.0-rc1-prep-20260902"
$workflow = "AURP v1.0.0-rc1"

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

git diff --check
if ($LASTEXITCODE -ne 0) {
    throw "git diff --check failed."
}

$dirty = @(git status --short --untracked-files=all)
if (-not $dirty) {
    git commit --allow-empty -m $Message
} else {
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
git push origin $branch
if ($LASTEXITCODE -ne 0) {
    throw "git push failed."
}

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
gh run watch $runId --exit-status
if ($LASTEXITCODE -ne 0) {
    gh run view $runId --log-failed
    throw "v1.0.0-rc1 gate failed for $head."
}

$downloadScript = Join-Path $PSScriptRoot "Download-RC1.ps1"
& $downloadScript -RunId ([long]$runId) -Destination $Destination
if ($LASTEXITCODE -ne 0) {
    throw "RC1 artifact download helper failed."
}

Write-Host "=== v1.0.0-rc1 EXACT-SHA BUILD COMPLETE ==="
Write-Host "Commit: $head"
Write-Host "Run ID: $runId"
