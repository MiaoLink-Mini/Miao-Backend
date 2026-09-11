param([ValidateSet('start','stop','status')][string]$Action='start', [int]$Port=55432)
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runtime=[IO.Path]::GetFullPath((Join-Path $root '.runtime'))
$data=[IO.Path]::GetFullPath((Join-Path $runtime 'postgres'))
if (!$data.StartsWith($root+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Database path escaped workspace' }
$bin=$env:POSTGRES_BIN
if (!$bin) { $bin=Join-Path $env:LOCALAPPDATA 'WeAgent\postgres-18.3\pgsql\bin' }
if (!(Test-Path -LiteralPath (Join-Path $bin 'pg_ctl.exe'))) { throw 'Set POSTGRES_BIN to native PostgreSQL bin directory. See docs/running.md.' }
New-Item -ItemType Directory -Force -Path $runtime | Out-Null
$config=Join-Path $runtime 'database.json'
if ($Action -eq 'stop') { & (Join-Path $bin 'pg_ctl.exe') -D $data -m fast -w stop; exit $LASTEXITCODE }
if ($Action -eq 'status') { & (Join-Path $bin 'pg_ctl.exe') -D $data status; exit $LASTEXITCODE }
if (!(Test-Path -LiteralPath (Join-Path $data 'PG_VERSION'))) {
  $bytes=New-Object byte[] 32; [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
  $password=[Convert]::ToBase64String($bytes)
  $pw=Join-Path $runtime 'init-password.txt'; [IO.File]::WriteAllText($pw,$password,[Text.UTF8Encoding]::new($false))
  try { & (Join-Path $bin 'initdb.exe') -D $data -U weagent --encoding=UTF8 --locale=C --auth=scram-sha-256 --pwfile=$pw | Out-Null; if ($LASTEXITCODE -ne 0) { throw 'initdb failed' } }
  finally { if (Test-Path -LiteralPath $pw) { Remove-Item -LiteralPath $pw } }
  $url='postgres://weagent:'+ [Uri]::EscapeDataString($password) +'@127.0.0.1:'+ $Port +'/postgres?sslmode=disable'
  [IO.File]::WriteAllText($config,(@{adminUrl=$url;port=$Port}|ConvertTo-Json),[Text.UTF8Encoding]::new($false))
}
if (!(Test-Path -LiteralPath $config)) { throw 'Existing cluster lacks private database.json; refusing to recreate credentials' }
$stored=Get-Content -LiteralPath $config -Raw | ConvertFrom-Json
if ($stored.port -ne $Port) { throw 'Use the original configured port for this cluster' }
& (Join-Path $bin 'pg_ctl.exe') -D $data status *> $null
if ($LASTEXITCODE -ne 0) {
  if (Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue) { throw 'Requested database port already occupied' }
  $arguments=@('-D',('"'+$data+'"'),'-l',('"'+(Join-Path $runtime 'postgres.log')+'"'),'-o',('"-h 127.0.0.1 -p '+$Port+'"'),'-w','start')
  $p=Start-Process -FilePath (Join-Path $bin 'pg_ctl.exe') -ArgumentList $arguments -WindowStyle Hidden -PassThru
  $p.WaitForExit()
  if ($p.ExitCode -ne 0) { throw 'PostgreSQL startup failed; inspect .runtime/postgres.log' }
}
$pidFile=Join-Path $data 'postmaster.pid'
Write-Output ('PostgreSQL fixture ready at 127.0.0.1:'+ $Port +'; PID '+(Get-Content -LiteralPath $pidFile -TotalCount 1)+'; credentials stored only in ignored .runtime/database.json')
