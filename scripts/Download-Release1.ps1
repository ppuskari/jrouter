param(
    [long]$RunId = 0,
    [string]$Destination = '.\build\v1.0.0'
)

$ErrorActionPreference = 'Stop'
$branch = 'release/globaltalk-aurp-router-v1.0.0'
$workflow = 'GlobalTalk AURP Router 1.0.0'
$linuxArtifact = 'globaltalk-aurp-router-v1.0.0-linux-amd64'
$windowsArtifact = 'globaltalk-aurp-router-v1.0.0-windows-amd64'

$repo = (git rev-parse --show-toplevel).Trim()
if (-not $repo) { throw 'Run this script from inside the jrouter repository.' }
Set-Location $repo
Get-Command gh -ErrorAction Stop | Out-Null

if ($RunId -eq 0) {
    $api = gh api --method GET repos/ppuskari/jrouter/actions/runs -f branch=$branch -f per_page=50 | ConvertFrom-Json
    $run = $api.workflow_runs |
        Where-Object { $_.name -eq $workflow -and $_.conclusion -eq 'success' } |
        Sort-Object { [datetime]$_.created_at } -Descending |
        Select-Object -First 1
} else {
    $run = gh api "repos/ppuskari/jrouter/actions/runs/$RunId" | ConvertFrom-Json
    if ($run.name -ne $workflow) { throw "Run $RunId is '$($run.name)', not '$workflow'." }
    if ($run.conclusion -ne 'success') { throw "Run $RunId conclusion is '$($run.conclusion)', not success." }
}
if (-not $run) { throw "No successful '$workflow' run found on $branch." }

$selectedRunId = [string]$run.id
$headSha = [string]$run.head_sha
if (Test-Path $Destination) { Remove-Item -Recurse -Force $Destination }
New-Item -ItemType Directory -Force -Path $Destination | Out-Null

foreach ($artifact in @($linuxArtifact, $windowsArtifact)) {
    gh run download $selectedRunId --name $artifact --dir (Join-Path $Destination $artifact)
    if ($LASTEXITCODE -ne 0) { throw "Artifact download failed: $artifact" }
}

$linuxDir = Join-Path $Destination $linuxArtifact
$windowsDir = Join-Path $Destination $windowsArtifact
$linux = Join-Path $linuxDir $linuxArtifact
$windows = Join-Path $windowsDir ($windowsArtifact + '.exe')

function Assert-Hash([string]$File, [string]$HashFile) {
    $actual = (Get-FileHash $File -Algorithm SHA256).Hash.ToLowerInvariant()
    $expected = (((Get-Content $HashFile | Select-Object -First 1) -split '\s+')[0]).ToLowerInvariant()
    if ($actual -ne $expected) { throw "SHA256 mismatch for $File" }
    return $actual
}

$linuxHash = Assert-Hash $linux (Join-Path $linuxDir ($linuxArtifact + '.sha256'))
$windowsHash = Assert-Hash $windows (Join-Path $windowsDir ($windowsArtifact + '.sha256'))

$linuxVersion = (Get-Content (Join-Path $linuxDir 'VERSION-LINUX.txt') -Raw).Trim()
$windowsVersion = (Get-Content (Join-Path $windowsDir 'VERSION-WINDOWS.txt') -Raw).Trim()
$expectedVersion = "jrouter v1.0.0 build $headSha"
if ($linuxVersion -ne $expectedVersion) { throw "Linux version proof mismatch: $linuxVersion" }
if ($windowsVersion -ne $expectedVersion) { throw "Windows version proof mismatch: $windowsVersion" }

@(
    "workflow=$workflow"
    "run_id=$selectedRunId"
    "head_sha=$headSha"
    "linux_sha256=$linuxHash"
    "windows_sha256=$windowsHash"
) | Set-Content -Path (Join-Path $Destination 'BUILD-INFO.txt') -Encoding ASCII

Write-Host '=== GLOBALTALK AURP ROUTER 1.0 FINAL ARTIFACTS READY ==='
Write-Host "Run ID:   $selectedRunId"
Write-Host "Commit:   $headSha"
Write-Host "Linux:    $linux"
Write-Host "Windows:  $windows"
Write-Host "Version:  $expectedVersion"
