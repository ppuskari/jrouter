param(
    [long]$RunId = 0,
    [string]$Destination = ".\build\v1.0.0-rc1"
)

$ErrorActionPreference = "Stop"

$branch = "petar/aurp-v1.0.0-rc1-prep-20260902"
$workflow = "AURP v1.0.0-rc1"
$artifact = "jrouter-v1.0.0-rc1-linux-amd64"

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

if (Test-Path $Destination) {
    Remove-Item -Recurse -Force $Destination
}
New-Item -ItemType Directory -Force -Path $Destination | Out-Null

gh run download $selectedRunId --name $artifact --dir $Destination
if ($LASTEXITCODE -ne 0) {
    throw "Artifact download failed for run $selectedRunId."
}

$binary = Join-Path $Destination "jrouter-v1.0.0-rc1-linux-amd64"
$shaFile = Join-Path $Destination "jrouter-v1.0.0-rc1-linux-amd64.sha256"
$versionFile = Join-Path $Destination "VERSION-OUTPUT.txt"
$infoFile = Join-Path $Destination "BUILD-INFO.txt"

if (-not (Test-Path $binary)) {
    throw "Downloaded RC binary not found: $binary"
}
if (-not (Test-Path $shaFile)) {
    throw "Downloaded RC SHA256 file not found: $shaFile"
}
if (-not (Test-Path $versionFile)) {
    throw "Downloaded RC version proof not found: $versionFile"
}

$localHash = (Get-FileHash $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$shaLine = Get-Content $shaFile | Select-Object -First 1
$expectedHash = (($shaLine -split "\s+")[0]).ToLowerInvariant()
if ($localHash -ne $expectedHash) {
    throw "SHA256 mismatch: local=$localHash CI=$expectedHash"
}

$versionOutput = (Get-Content $versionFile | Select-Object -First 1).Trim()
$expectedVersion = "jrouter v1.0.0-rc1 build $headSha"
if ($versionOutput -ne $expectedVersion) {
    throw "Version proof mismatch: '$versionOutput' expected '$expectedVersion'"
}

@(
    "workflow=$workflow"
    "run_id=$selectedRunId"
    "head_sha=$headSha"
    "artifact=$artifact"
    "version=$versionOutput"
    "sha256=$localHash"
) | Set-Content -Path $infoFile -Encoding ASCII

Write-Host "=== JROUTER v1.0.0-rc1 ARTIFACT READY ==="
Write-Host "Run ID:  $selectedRunId"
Write-Host "Commit:  $headSha"
Write-Host "Binary:  $binary"
Write-Host "Version: $versionOutput"
Write-Host "SHA256:  $localHash"
Write-Host "Info:    $infoFile"
