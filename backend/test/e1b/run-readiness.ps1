# Extension of the existing E1A/I2.2 harness, not a CI framework.
param(
 [Parameter(Mandatory=$true)][string]$EvidenceDirectory,
 [Parameter(Mandatory=$true)][string]$TestBinary,
 [string]$ApiTestBinary='',
 [switch]$PopulateTestData,
 [string]$DockerPath='C:\Users\tcat\AppData\Local\Programs\DockerDesktop\resources\bin\docker.exe'
)
$ErrorActionPreference='Stop'
$docker=$DockerPath
$workspace=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../../..'))
$evidence=[IO.Path]::GetFullPath($EvidenceDirectory)
if (!(Test-Path -LiteralPath $evidence -PathType Container) -or !(Test-Path -LiteralPath $TestBinary -PathType Leaf)){throw 'Existing TEST artifact directory and freshly compiled Linux binary required'}
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
$compose=@('compose','--env-file','NUL','-p',$env:E1A_RUN_ID,'-f',(Join-Path $workspace 'backend/test/e1a/compose.yaml'),'-f',(Join-Path $workspace 'backend/test/e1b/compose.execution.yaml'),'-f',(Join-Path $workspace 'backend/test/e1b/compose.readiness.yaml'))
if ($ApiTestBinary) {
 if (!(Test-Path -LiteralPath $ApiTestBinary -PathType Leaf)){throw 'Fresh API TEST binary required'}
 $env:I3_API_BINARY=[IO.Path]::GetFullPath($ApiTestBinary)
 $env:I3_READ_POLICY='yes'
 $compose+=@('-f',(Join-Path $workspace 'backend/test/e1b/compose.api.yaml'))
} else {$env:I3_READ_POLICY='no'}
$runID=$env:E1A_RUN_ID
$runExit=-1
try {
 & $docker image inspect $env:E1A_TEST_IMAGE --format '{{.Id}} {{json .RepoDigests}}' | Set-Content (Join-Path $evidence ($runID+'-image.txt'))
 if ($LASTEXITCODE -ne 0){throw 'Pinned local TEST image unavailable'}
 & $docker @compose up --no-build -d pg
 if ($LASTEXITCODE -ne 0){throw 'TEST PostgreSQL failed'}
 & $docker @compose run --rm tests 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-setup.log'))
 if ($LASTEXITCODE -ne 0){throw 'TEST setup failed'}
 & $docker @compose up --no-build -d verifier
 if ($LASTEXITCODE -ne 0){throw 'Verifier start failed'}
 & $docker @compose run --rm api 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-api.log'))
 if ($LASTEXITCODE -ne 0){throw 'API TEST failed'}
 if ($ApiTestBinary) {
 & $docker @compose up --no-build -d api-http
 if ($LASTEXITCODE -ne 0){throw 'HTTP API start failed'}
 $faultArgs=@('run','--rm','-e','I3_HTTP_ASSERT=yes','faults')
 } else {$faultArgs=@('run','--rm','faults')}
 & $docker @compose @faultArgs 2>&1 | Tee-Object -FilePath (Join-Path $evidence ($runID+'-faults.log'))
 if ($LASTEXITCODE -ne 0){throw 'Fault TEST failed'}
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
}
if ($runExit -ne 0){throw "Readiness TEST failed: exit $runExit"}
