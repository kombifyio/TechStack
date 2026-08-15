$ErrorActionPreference = "Continue"
Set-Location $PSScriptRoot
$env:PLAYWRIGHT_BASE_URL = "http://localhost:5261"
$env:TECHSTACK_API_URL = "http://localhost:5260"
$env:API_URL = "http://localhost:5260"
$env:TECHSTACK_URL = "http://localhost:5260"
Write-Host "=== Running Playwright tests ==="
Write-Host "CWD: $(Get-Location)"
Write-Host "PLAYWRIGHT_BASE_URL: $env:PLAYWRIGHT_BASE_URL"
Write-Host "TECHSTACK_API_URL: $env:TECHSTACK_API_URL"
pnpm exec playwright test --config=playwright.nosetup.config.ts --reporter=list --project=chromium 2>&1
Write-Host "=== Exit code: $LASTEXITCODE ==="
