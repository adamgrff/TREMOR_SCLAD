# Extension of the existing E1A/I2.2 harness, not a CI framework.
param(
 [Parameter(Mandatory=$true)][string]$EvidenceDirectory,
 [Parameter(Mandatory=$true)][string]$TestBinary,
 [string]$ApiTestBinary='',
 [switch]$PopulateTestData,
 [switch]$HistoryRegression,
 [switch]$Acceptance,
 [string]$DockerPath='C:\Users\tcat\AppData\Local\Programs\DockerDesktop\resources\bin\docker.exe'
)
$ErrorActionPreference='Stop'
$docker=$DockerPath
$workspace=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../..'))
$evidence=[IO.Path]::GetFullPath($EvidenceDirectory)
if (!(Test-Path -LiteralPath $evidence -PathType Container) -or !(Test-Path -LiteralPath $TestBinary -PathType Leaf)){throw 'Existing TEST artifact directory and freshly compiled Linux binary required'}
if($HistoryRegression -and !$ApiTestBinary){throw 'History regression requires real API TEST binary'}
if($Acceptance -and !$ApiTestBinary){throw 'Acceptance requires real API TEST binary'}
$env:E1B_UPGRADE=if($PopulateTestData){'1'}else{'0'}
$env:E1A_RUN_ID='e1b-readiness-'+[guid]::NewGuid().ToString('N')
$env:E1A_TEST_IMAGE='tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2'
$env:E1A_GO_IMAGE='docker.io/library/golang@sha256:025eefc1e8dbe63201734a72aaa0a55d7a077f2d8410432f8ae2f3e939f32871'
$env:E1A_PG_IMAGE='docker.io/library/postgres@sha256:02fb3245e038f33536cf13bc6c4e59f21d07b0cea3c1fe1c07c6e8aa8f083bcc'
$env:E1A_TEST_PASSWORD=[Convert]::ToBase64String([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:E1A_T1_APPROVED='yes'
$env:E1B_TEST_BINARY=[IO.Path]::GetFullPath($TestBinary)
$env:I3_ROOT=Join-Path $evidence ($env:E1A_RUN_ID+'-private')
New-Item -ItemType Directory -Path (Join-Path $env:I3_ROOT 'api'),(Join-Path $env:I3_ROOT 'verifier') | Out-Null
if($Acceptance){New-Item -ItemType Directory -Path (Join-Path $env:I3_ROOT 'control') | Out-Null; $env:E1A_RESTORE_PASSWORD=[Convert]::ToBase64String([System.Security.Cryptography.RandomNumberGenerator]::GetBytes(32))}
$compose=@('compose','--env-file','NUL','-p',$env:E1A_RUN_ID,'-f',(Join-Path $workspace 'backend/test/e1a/compose.yaml'),'-f',(Join-Path $workspace 'backend/test/e1b/compose.execution.yaml'),'-f',(Join-Path $workspace 'backend/test/e1b/compose.readiness.yaml'))
if ($ApiTestBinary) {
 if (!(Test-Path -LiteralPath $ApiTestBinary -PathType Leaf)){throw 'Fresh API TEST binary required'}
 $env:I3_API_BINARY=[IO.Path]::GetFullPath($ApiTestBinary)
 $env:I3_READ_POLICY='yes'
 $compose+=@('-f',(Join-Path $workspace 'backend/test/e1b/compose.api.yaml'))
} else {$env:I3_READ_POLICY='no'}
if($Acceptance){$compose+=@('-f',(Join-Path $workspace 'backend/test/e1b/compose.acceptance.yaml'))}
$runID=$env:E1A_RUN_ID
$runExit=-1

# Supervise only our TEST command. No detached helper or host port is created.
function Invoke-I4Command([string[]]$DockerArgs,[string]$LogName) {
 $info=[Diagnostics.ProcessStartInfo]::new($docker)
 $info.UseShellExecute=$false; $info.CreateNoWindow=$true
 $info.RedirectStandardOutput=$true; $info.RedirectStandardError=$true
 foreach($arg in ($compose+$DockerArgs)){$info.ArgumentList.Add($arg)}
 $process=[Diagnostics.Process]::Start($info)
 $stdout=$process.StandardOutput.ReadToEndAsync();$stderr=$process.StandardError.ReadToEndAsync()
 try {
  while(!$process.WaitForExit(250)) {
   foreach($phase in @('before-commit','after-commit')) {
    $request=Join-Path $env:I3_ROOT "control/$phase.request"; $ack=Join-Path $env:I3_ROOT "control/$phase.ack"
    if((Test-Path -LiteralPath $request) -and !(Test-Path -LiteralPath $ack)) {
     $expected=[IO.File]::ReadAllText($request)
     if($expected -notmatch '^[1-9][0-9]{0,19}$'){throw 'Invalid TEST system identifier'}
     $id=& $docker @compose ps -q pg
     $label=& $docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "tremor.e1a.run"}}' $id
     if($label -ne "$runID|$runID"){throw 'Crash refused: own TEST project identity mismatch'}
     $actual=(& $docker @compose exec -T pg psql -U e1a_test_admin -d tremor_e1b_collector_test -Atqc 'SELECT system_identifier::text FROM pg_control_system()').Trim()
     if($LASTEXITCODE -or $actual -ne $expected){throw 'Crash refused: TEST system identity mismatch'}
     "FAULT SIGKILL phase=$phase system=$actual" | Add-Content (Join-Path $evidence ($runID+'-crash.log'))
     & $docker @compose exec -T -u postgres pg /bin/bash -c 'read -r pid < "$PGDATA/postmaster.pid"; test "$pid" -gt 1 && kill -KILL "$pid"'
     if($LASTEXITCODE){throw 'TEST postmaster SIGKILL failed'}
     $recovered=$false
     for($i=0;$i -lt 80;$i++) {
      Start-Sleep -Milliseconds 250
      $again=& $docker @compose exec -T pg psql -U e1a_test_admin -d tremor_e1b_collector_test -Atqc 'SELECT system_identifier::text FROM pg_control_system()' 2>$null
      if($LASTEXITCODE -eq 0 -and "$again".Trim() -eq $expected){$recovered=$true;break}
     }
     if(!$recovered){throw 'TEST WAL recovery did not finish'}
     [IO.File]::WriteAllText($ack,$expected)
    }
   }
  }
  $content=$stdout.GetAwaiter().GetResult()+$stderr.GetAwaiter().GetResult()
  $content | Tee-Object -FilePath (Join-Path $evidence ($runID+'-'+$LogName+'.log'))
  if($process.ExitCode -ne 0){throw "Acceptance TEST command failed: $LogName exit=$($process.ExitCode)"}
 } finally {if(!$process.HasExited){$process.Kill($true)};$process.Dispose()}
}
try {
 & $docker image inspect $env:E1A_TEST_IMAGE --format '{{.Id}} {{json .RepoDigests}}' | Set-Content (Join-Path $evidence ($runID+'-image.txt'))
 if ($LASTEXITCODE -ne 0){throw 'Pinned local TEST image unavailable'}
 & $docker @compose up --no-build --no-recreate -d pg
 if ($LASTEXITCODE -ne 0){throw 'TEST PostgreSQL failed'}
 if($Acceptance){
  $started=$false
  for($i=0;$i -lt 100;$i++){$answer=& $docker @compose exec -T pg psql -U e1a_test_admin -d tremor_e1b_collector_test -Atqc 'SELECT 1' 2>$null;if($LASTEXITCODE -eq 0 -and "$answer".Trim() -eq '1'){$started=$true;break};Start-Sleep -Milliseconds 250}
  if(!$started){throw 'Fresh TEST database startup failed'}
  Invoke-I4Command @('run','--no-deps','--rm','tests') 'setup'
 } else {
  & $docker @compose run --rm tests 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-setup.log'))
  if ($LASTEXITCODE -ne 0){throw 'TEST setup failed'}
 }
 & $docker @compose up --no-build --no-recreate -d verifier
 if ($LASTEXITCODE -ne 0){throw 'Verifier start failed'}
 & $docker @compose run --rm api 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-api.log'))
 if ($LASTEXITCODE -ne 0){throw 'API TEST failed'}
 if ($ApiTestBinary) {
 & $docker @compose up --no-build --no-recreate -d api-http
 if ($LASTEXITCODE -ne 0){throw 'HTTP API start failed'}
 $faultArgs=@('run','--rm','-e','I3_HTTP_ASSERT=yes','faults')
 if($HistoryRegression){$faultArgs+=@('-test.v','-test.timeout=120s','-test.run=^TestI4HistoricalLedgerTimestampDriftDisposable$')}
 } else {$faultArgs=@('run','--rm','faults')}
 & $docker @compose @faultArgs 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-faults.log'))
 if ($LASTEXITCODE -ne 0){throw 'Fault TEST failed'}
 if($Acceptance){
  Invoke-I4Command @('run','--rm','-e','I3_HTTP_ASSERT=yes','faults','-test.v','-test.timeout=120s','-test.run=^TestI4HistoricalLedgerTimestampDriftDisposable$') 'history'
  Invoke-I4Command @('run','--rm','-e','I3_HTTP_ASSERT=yes','faults','-test.v','-test.timeout=120s','-test.run=^TestI4AcceptanceRuntimeDisposable$') 'crash-durable'
  & $docker @compose exec -T pg pg_dumpall -U e1a_test_admin --globals-only --no-role-passwords -f /tmp/i4-roles.sql
  if($LASTEXITCODE){throw 'TEST globals backup failed'}
  & $docker @compose exec -T pg pg_dump -U e1a_test_admin -d tremor_e1b_collector_test -Fc -f /tmp/i4.dump
  if($LASTEXITCODE){throw 'TEST data backup failed'}
  $backup=Join-Path $env:I3_ROOT 'control/backup';New-Item -ItemType Directory -Path $backup | Out-Null
  & $docker @compose cp pg:/tmp/i4-roles.sql (Join-Path $backup 'roles.sql')
  if($LASTEXITCODE){throw 'TEST globals copy failed'}
  & $docker @compose cp pg:/tmp/i4.dump (Join-Path $backup 'test.dump')
  if($LASTEXITCODE){throw 'TEST dump copy failed'}
  & $docker @compose up --no-build -d restore-pg
  if($LASTEXITCODE){throw 'Restore TEST cluster failed'}
  for($i=0;$i -lt 80;$i++){& $docker @compose exec -T restore-pg pg_isready -U e1a_restore_admin -d tremor_e1b_collector_test *> $null;if($LASTEXITCODE -eq 0){break};Start-Sleep -Milliseconds 250}
  & $docker @compose cp (Join-Path $backup 'roles.sql') restore-pg:/tmp/i4-roles.sql
  if($LASTEXITCODE){throw 'Restore globals copy failed'}
  & $docker @compose cp (Join-Path $backup 'test.dump') restore-pg:/tmp/i4.dump
  if($LASTEXITCODE){throw 'Restore dump copy failed'}
  # The pinned TEST image creates this single, unused fixture role at initdb.
  # Remove only that role in the fresh own restore cluster before exact globals.
  & $docker @compose exec -T restore-pg psql -X -v ON_ERROR_STOP=1 -U e1a_restore_admin -d tremor_e1b_collector_test -c 'DROP ROLE e1a_alternate'
  if($LASTEXITCODE){throw 'Fresh restore fixture role preparation failed'}
  & $docker @compose exec -T restore-pg psql -X -v ON_ERROR_STOP=1 -U e1a_restore_admin -d tremor_e1b_collector_test -f /tmp/i4-roles.sql *> (Join-Path $evidence ($runID+'-restore-roles.log'))
  if($LASTEXITCODE){throw 'Restore globals failed'}
  & $docker @compose exec -T restore-pg pg_restore --exit-on-error -U e1a_restore_admin -d tremor_e1b_collector_test /tmp/i4.dump
  if($LASTEXITCODE){throw 'Restore DB failed'}
  # pg_dump does not restore this system-catalog function ACL. Explicit minimal
  # TEST restore procedure; this does not approve the restored target/anchors.
  & $docker @compose exec -T restore-pg psql -X -v ON_ERROR_STOP=1 -U e1a_restore_admin -d tremor_e1b_collector_test -c 'GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO tremor_migrator'
  if($LASTEXITCODE){throw 'Restore minimal verifier READ identity ACL failed'}
  & $docker @compose run --rm restore-tests 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-restore.log'))
  if($LASTEXITCODE){throw 'Restore assertions failed'}
 }
 & $docker @compose logs verifier 2>&1 | Set-Content (Join-Path $evidence ($runID+'-verifier.log'))
 & $docker @compose stop verifier
 $offlineArgs=@('run','--rm','-e','I3_MODE=offline')
 if($ApiTestBinary){$offlineArgs+=@('-e','I3_HTTP_ASSERT=yes')}
 $offlineArgs+=@('api','-test.v','-test.timeout=15s','-test.run=^TestI3OfflineIntegrationDisposable$')
 & $docker @compose @offlineArgs 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-offline.log'))
 if ($LASTEXITCODE -ne 0){throw 'Offline API TEST failed'}
 $runExit=$LASTEXITCODE
 Write-Output "TEST_RUN=$runID TEST_EXIT=$runExit"
} finally {
 & $docker @compose logs pg 2>&1 | Set-Content (Join-Path $evidence ($runID+'-postgres.log'))
 if ($ApiTestBinary) {& $docker @compose logs api-http 2>&1 | Set-Content (Join-Path $evidence ($runID+'-api-http.log'))}
 & $docker @compose logs verifier 2>&1 | Set-Content (Join-Path $evidence ($runID+'-verifier.log'))
 $ids=@(& $docker @compose ps -a -q)
 if ($LASTEXITCODE -ne 0){throw 'Cannot enumerate own TEST containers'}
 foreach ($id in $ids) {
  if (!$id){continue}
  $label=& $docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}|{{index .Config.Labels "tremor.e1a.run"}}' $id
  if ($LASTEXITCODE -ne 0 -or $label -ne "$runID|$runID"){throw 'Cleanup refused: TEST resource identity mismatch'}
  & $docker inspect --format '{{.Id}} image={{.Config.Image}} network={{.HostConfig.NetworkMode}} ports={{json .HostConfig.PortBindings}} oom={{.State.OOMKilled}} exit={{.State.ExitCode}}' $id | Add-Content (Join-Path $evidence ($runID+'-runtime.txt'))
 }
 $networks=@(& $docker network ls -q --filter "label=com.docker.compose.project=$runID")
 foreach ($network in $networks){if($network){& $docker network inspect --format '{{.Name}} internal={{.Internal}} project={{index .Labels "com.docker.compose.project"}}' $network | Add-Content (Join-Path $evidence ($runID+'-runtime.txt'))}}
 $ids | Set-Content (Join-Path $evidence ($runID+'-container-ids.txt'))
 & $docker @compose down --volumes 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-cleanup.log'))
 if ($LASTEXITCODE -ne 0){throw 'Own TEST cleanup failed'}
 $remainingContainers=@(& $docker ps -a -q --filter "label=com.docker.compose.project=$runID")
 $remainingNetworks=@(& $docker network ls -q --filter "label=com.docker.compose.project=$runID")
 $remainingVolumes=@(& $docker volume ls -q --filter "label=com.docker.compose.project=$runID")
 if ($remainingContainers.Count -or $remainingNetworks.Count -or $remainingVolumes.Count){throw 'Own TEST resources remain'}
 "CLEANUP_COMPLETE project=$runID containers=0 networks=0 volumes=0 test_exit=$runExit" | Tee-Object -FilePath (Join-Path $evidence ($runID+'-result.txt'))
 $resolved=[IO.Path]::GetFullPath($env:I3_ROOT)
 if ($resolved -ne (Join-Path $evidence ($runID+'-private'))){throw 'Secret cleanup target mismatch'}
 Remove-Item -LiteralPath $resolved -Recurse -Force
 Remove-Item Env:I3_ROOT
 Remove-Item Env:I3_READ_POLICY -ErrorAction SilentlyContinue
 Remove-Item Env:I3_API_BINARY -ErrorAction SilentlyContinue
 Remove-Item Env:E1A_TEST_PASSWORD
 Remove-Item Env:E1A_RESTORE_PASSWORD -ErrorAction SilentlyContinue
}
if ($runExit -ne 0){throw "Readiness TEST failed: exit $runExit"}
