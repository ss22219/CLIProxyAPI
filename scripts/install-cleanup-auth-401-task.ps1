param(
  [string]$TaskName = "CLIProxyAPI Cleanup 401 Auth Files",
  [string]$BaseUrl = "http://127.0.0.1:8317",
  [string]$ManagementKey = $env:CLIPROXYAPI_MANAGEMENT_KEY,
  [int]$IntervalMinutes = 10
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($ManagementKey)) {
  throw "ManagementKey is required. Pass -ManagementKey or set CLIPROXYAPI_MANAGEMENT_KEY."
}

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$scriptPath = Join-Path $repoRoot "scripts\cleanup-auth-401.js"
if (!(Test-Path $scriptPath)) {
  throw "cleanup script not found: $scriptPath"
}

$node = (Get-Command node -ErrorAction Stop).Source
$escapedScript = $scriptPath.Replace('"', '\"')
$escapedBaseUrl = $BaseUrl.Replace('"', '\"')
$escapedKey = $ManagementKey.Replace('"', '\"')
$arguments = "`"$escapedScript`" --base-url `"$escapedBaseUrl`" --key `"$escapedKey`""

$action = New-ScheduledTaskAction -Execute $node -Argument $arguments -WorkingDirectory $repoRoot
$trigger = New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(1) `
  -RepetitionInterval (New-TimeSpan -Minutes $IntervalMinutes) `
  -RepetitionDuration (New-TimeSpan -Days 3650)
$settings = New-ScheduledTaskSettingsSet `
  -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries `
  -StartWhenAvailable `
  -MultipleInstances IgnoreNew

Register-ScheduledTask `
  -TaskName $TaskName `
  -Action $action `
  -Trigger $trigger `
  -Settings $settings `
  -Description "Periodically deletes CLIProxyAPI auth files that report HTTP 401/auth_unavailable/token invalidated." `
  -Force | Out-Null

Write-Host "Installed scheduled task: $TaskName"
Write-Host "Interval: every $IntervalMinutes minutes"
Write-Host "Script: $scriptPath"
