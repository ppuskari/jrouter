param(
    [long]$RunId = 0,
    [string]$Destination = ".\build\set28-latest"
)

$ErrorActionPreference = "Stop"

$branch = "petar/aurp-set28-rc-prep-20260901"
$workflow = "AURP Set28 RC Prep"
$artifact = "jrouter-set28-linux-amd64"

$repo = (git rev-parse --show-toplevel).Trim()
if (-not $repo) {
    throw "Run this script from inside the jrouter repository."
}
Set-Location $repo

Get-Command gh -ErrorAction Stop | Out-Null

if ($RunId -eq 0) {
    $api = gh api --method GET repos/ppuskari/jrouter/actions/runs -f branch=$branch -f per_page=50 | ConvertFrom-Json
    $run = $api.workflow_runs |
        Where-Object {
            $_.name -eq $workflow -and
            $_.conclusion -eq "success"
        } |
        Sort-Object { [datetime]$_.created_at } -Descending |
        Select-Object -First 1
} else {
    $run = gh api "repos/ppuskari/jrouter/actions/runs/$RunId" | ConvertFrom-Json
    if ($run.name -ne $workflow) {
        throw "Run $RunId is '$($run.name)', not '$workflow'."
    }
    if ($run.conclusion -ne "success") {
        throw "Run $RunId conclusion is '$($run.conclusion)', not success."
    }
}

if (-not $run) {
    throw "No successful '$workflow' run found on $branch."
}

$selectedRunId = [string]$run.id
$headSha = [string]$run.head_sha

Write-Host ""
Write-Host "Set28 run:  $selectedRunId"
Write-Host "Commit:     $headSha"
Write-Host "Artifact:   $artifact"
Write-Host "Destination $Destination"
Write-Host ""

if (Test-Path $Destination) {
    Remove-Item -Recurse -Force $Destination
}
New-Item -ItemType Directory -Force -Path $Destination | Out-Null

# A single scalar run ID is passed deliberately. This avoids PowerShell
# expanding an array of run IDs into multiple positional gh arguments.
gh run download $selectedRunId --name $artifact --dir $Destination
if ($LASTEXITCODE -ne 0) {
    throw "Artifact download failed for run $selectedRunId."
}

$binary = Join-Path $Destination "jrouter-set28-linux-amd64"
$shaFile = Join-Path $Destination "jrouter-set28-linux-amd64.sha256"
$infoFile = Join-Path $Destination "BUILD-INFO.txt"

if (-not (Test-Path $binary)) {
    throw "Downloaded Set28 binary not found: $binary"
}
if (-not (Test-Path $shaFile)) {
    throw "Downloaded Set28 SHA256 file not found: $shaFile"
}

$localHash = (Get-FileHash $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$shaLine = Get-Content $shaFile | Select-Object -First 1
$expectedHash = (($shaLine -split "\s+")[0]).ToLowerInvariant()

if ($localHash -ne $expectedHash) {
    throw "SHA256 mismatch: local=$localHash CI=$expectedHash"
}

@(
    "workflow=$workflow"
    "run_id=$selectedRunId"
    "head_sha=$headSha"
    "artifact=$artifact"
    "sha256=$localHash"
) | Set-Content -Path $infoFile -Encoding ASCII

Write-Host "=== SET28 ARTIFACT READY ==="
Write-Host "Run ID:  $selectedRunId"
Write-Host "Commit:  $headSha"
Write-Host "Binary:  $binary"
Write-Host "SHA256:  $localHash"
Write-Host "Info:    $infoFile"
