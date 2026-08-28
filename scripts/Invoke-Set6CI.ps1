param(
    [Parameter(Mandatory = $true)]
    [string]$Message
)

$ErrorActionPreference = "Stop"

$branch = "petar/aurp-set6-field-20260828"
$workflow = "AURP Set-6 Test Build"
$artifact = "jrouter-set6-linux-amd64"
$dest = ".\\build\\set6-ci"

$repo = (git rev-parse --show-toplevel).Trim()
Set-Location $repo

$current = (git branch --show-current).Trim()
if ($current -ne $branch) {
    throw "Wrong branch: $current"
}

$dirty = @(git status --short | Where-Object {
    $_ -notmatch '^\\?\\? build/'
})

if (-not $dirty) {
    Write-Host "No source changes to commit."
    Write-Host "Creating an empty CI trigger commit."
    git commit --allow-empty -m $Message
} else {
    Write-Host ""
    Write-Host "=== CHANGES ==="
    git status --short

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
Write-Host "Pushing $head ..."
git push origin $branch
if ($LASTEXITCODE -ne 0) {
    throw "git push failed."
}

Write-Host ""
Write-Host "Waiting for GitHub Actions run for $head ..."

$run = $null
for ($i = 0; $i -lt 45; $i++) {
    Start-Sleep -Seconds 2

    $runs = gh run list --workflow $workflow --branch $branch --limit 10 --json databaseId,status,conclusion,headSha,displayTitle | ConvertFrom-Json

    $run = $runs | Where-Object { $_.headSha -eq $head } | Select-Object -First 1

    if ($run) {
        break
    }
}

if (-not $run) {
    throw "No GitHub Actions run appeared for $head"
}

$runId = $run.databaseId

Write-Host ""
Write-Host "Run ID: $runId"
Write-Host ""

gh run watch $runId --exit-status
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "=== FAILED LOG ==="
    gh run view $runId --log-failed
    throw "GitHub Actions Set-6 gate failed."
}

if (Test-Path $dest) {
    Remove-Item -Recurse -Force $dest
}

New-Item -ItemType Directory -Force -Path $dest | Out-Null

gh run download $runId --name $artifact --dir $dest

if ($LASTEXITCODE -ne 0) {
    throw "Artifact download failed."
}

$binary = Join-Path $dest "jrouter-set6-linux-amd64"
$shaFile = Join-Path $dest "jrouter-set6-linux-amd64.sha256"

if (-not (Test-Path $binary)) {
    throw "Set-6 binary not found after artifact download."
}

if (-not (Test-Path $shaFile)) {
    throw "Set-6 SHA256 file not found after artifact download."
}

Write-Host ""
Write-Host "=== SET-6 BUILD READY ==="
Write-Host "Commit: $head"
Write-Host "Run ID: $runId"
Write-Host "Binary: $binary"
Write-Host ""
Write-Host "Local SHA256:"
Get-FileHash $binary -Algorithm SHA256
Write-Host ""
Write-Host "CI SHA256:"
Get-Content $shaFile
