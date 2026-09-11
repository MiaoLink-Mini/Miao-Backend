param([switch]$Race)
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
try {
  & (Join-Path $PSScriptRoot 'dev-db.ps1') start
  $private=Get-Content -LiteralPath (Join-Path $root '.runtime\database.json') -Raw | ConvertFrom-Json
  $env:TEST_DATABASE_URL=$private.adminUrl
  if ($Race) { go test -race -count=1 -timeout 180s ./... } else { go test -count=1 -timeout 180s ./... }
  $result=$LASTEXITCODE
  if ($result -ne 0) { throw "Go tests failed with exit $result" }
  node scripts/generate-contracts.cjs --check
  if ($LASTEXITCODE -ne 0) { throw 'Generated types drifted' }
  npm test
  if ($LASTEXITCODE -ne 0) { throw 'Contract fixture tests failed' }
}
finally {
  Remove-Item Env:\TEST_DATABASE_URL -ErrorAction SilentlyContinue
  & (Join-Path $PSScriptRoot 'dev-db.ps1') stop
  Pop-Location
}
