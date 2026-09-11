param([switch]$WithFakeNode, [int]$Port=8080, [int]$SmokeSeconds=0,
  [ValidateSet('wechat','development')][string]$AuthMode='wechat', [string]$WechatConfigFile='')
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $root
$gateway=$null; $node=$null; $dbStarted=$false
try {
  if ($AuthMode -eq 'wechat') {
    if (!$WechatConfigFile) { $WechatConfigFile=Join-Path $root '.runtime\wechat-auth.json' }
    if (Test-Path -LiteralPath $WechatConfigFile) {
      $wechat=Get-Content -LiteralPath $WechatConfigFile -Raw | ConvertFrom-Json
      $env:WECHAT_APP_ID=[string]$wechat.appId
      $env:WECHAT_APP_SECRET=[string]$wechat.appSecret
    }
    if (!$env:WECHAT_APP_ID -or !$env:WECHAT_APP_SECRET) { throw 'WeChat auth requires backend-only WECHAT_APP_ID/WECHAT_APP_SECRET or .runtime/wechat-auth.json (appId, appSecret); no development fallback.' }
  }
  if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) { throw 'Gateway port already occupied' }
  & (Join-Path $PSScriptRoot 'dev-db.ps1') start; $dbStarted=$true
  $private=Get-Content -LiteralPath (Join-Path $root '.runtime\database.json') -Raw | ConvertFrom-Json
  $pgbin=$env:POSTGRES_BIN
  if (!$pgbin) { $pgbin=Join-Path $env:LOCALAPPDATA 'WeAgent\postgres-18.3\pgsql\bin' }
  $uri=[Uri]$private.adminUrl
  $env:PGPASSWORD=[Uri]::UnescapeDataString($uri.UserInfo.Split(':',2)[1])
  $exists=& (Join-Path $pgbin 'psql.exe') -h 127.0.0.1 -p $private.port -U weagent -d postgres -At -c "SELECT 1 FROM pg_database WHERE datname='weagent_dev'"
  if ($LASTEXITCODE -ne 0) { throw 'Database discovery failed' }
  if ($exists -ne '1') { & (Join-Path $pgbin 'psql.exe') -h 127.0.0.1 -p $private.port -U weagent -d postgres -c 'CREATE DATABASE weagent_dev' | Out-Null; if ($LASTEXITCODE -ne 0) { throw 'Development database creation failed' } }
  Remove-Item Env:\PGPASSWORD
  $env:DATABASE_URL=$private.adminUrl.Replace('/postgres?','/weagent_dev?')
  $keyFile=Join-Path $root '.runtime\gateway-hmac-key'
  if (!(Test-Path -LiteralPath $keyFile)) { $bytes=New-Object byte[] 32;[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes);[IO.File]::WriteAllText($keyFile,[Convert]::ToBase64String($bytes)) }
  $env:GATEWAY_HMAC_KEY=[IO.File]::ReadAllText($keyFile)
  $env:AUTH_MODE=$AuthMode;$env:LISTEN_ADDR='127.0.0.1:'+$Port;$env:ALLOWED_ORIGINS='http://127.0.0.1:'+$Port+',http://localhost:'+$Port
  New-Item -ItemType Directory -Force -Path bin | Out-Null
  go build -trimpath -o bin/gateway.exe ./cmd/gateway
  if ($LASTEXITCODE -ne 0) { throw 'Gateway build failed' }
  go build -trimpath -o bin/migrate.exe ./cmd/migrate
  if ($LASTEXITCODE -ne 0) { throw 'Migration build failed' }
  & .\bin\migrate.exe up
  if ($LASTEXITCODE -ne 0) { throw 'Migration failed' }
  $gateway=Start-Process -FilePath (Join-Path $root 'bin\gateway.exe') -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $root '.runtime\gateway.stdout.log') -RedirectStandardError (Join-Path $root '.runtime\gateway.stderr.log')
  $ready=$false
  for ($i=0;$i -lt 50;$i++) { try { $health=Invoke-RestMethod -Uri ('http://127.0.0.1:'+$Port+'/readyz');if ($health.status -eq 'ok') {$ready=$true;break} } catch {}; if($gateway.HasExited){throw 'Gateway exited during startup'};Start-Sleep -Milliseconds 100 }
  if (!$ready) { throw 'Gateway readiness timeout' }
  Write-Output ('Gateway ready: http://127.0.0.1:'+$Port+'; PID '+$gateway.Id+'; auth mode '+$AuthMode+'; private state under .runtime')
  if ($WithFakeNode) {
    go build -trimpath -o bin/fake-node.exe ./cmd/fake-node
    if ($LASTEXITCODE -ne 0) { throw 'Fake Node build failed' }
    $node=Start-Process -FilePath (Join-Path $root 'bin\fake-node.exe') -ArgumentList @('-gateway',('http://127.0.0.1:'+$Port),'-state-dir',('"'+(Join-Path $root '.runtime\fake-node')+'"')) -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $root '.runtime\fake-node.stdout.log') -RedirectStandardError (Join-Path $root '.runtime\fake-node.stderr.log')
    Write-Output ('Fake Node PID '+$node.Id+'; pairing code is in ignored .runtime/fake-node.stdout.log')
  }
  if ($SmokeSeconds -gt 0) { Start-Sleep -Seconds $SmokeSeconds; $null=Invoke-RestMethod -Uri ('http://127.0.0.1:'+$Port+'/healthz');Write-Output 'Gateway process smoke check passed' }
  else { Write-Output 'Press Ctrl+C to stop recorded child PIDs and the fixture database.';while (!$gateway.HasExited) {Start-Sleep -Seconds 1} }
}
finally {
  if ($node -and !$node.HasExited) { Stop-Process -Id $node.Id -ErrorAction SilentlyContinue }
  if ($gateway -and !$gateway.HasExited) { Stop-Process -Id $gateway.Id -ErrorAction SilentlyContinue }
  foreach ($name in @('DATABASE_URL','GATEWAY_HMAC_KEY','AUTH_MODE','LISTEN_ADDR','PGPASSWORD')) {Remove-Item -LiteralPath ('Env:\'+$name) -ErrorAction SilentlyContinue}
  if ($dbStarted) { & (Join-Path $PSScriptRoot 'dev-db.ps1') stop }
  Pop-Location
}
