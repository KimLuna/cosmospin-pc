$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $Root
$Tools = Join-Path $Root ".tools"
$Runtime = Join-Path $Root ".runtime"
$GoDir = Join-Path $Tools "go"
$GoExe = Join-Path $GoDir "bin\go.exe"
$GoZip = Join-Path $Tools "go.zip"
$GoVersion = "1.26.8"
$GoVersionMarker = Join-Path $Tools "go-version.txt"
$GoUrl = "https://go.dev/dl/go$GoVersion.windows-amd64.zip"
$GoSha256 = "b92c3b2adae85a11ba71fe7216daf0d84e82af4c8ab6c5625807f28622043a59"
$BuildVersion = "v4-spin-20260925-5"

New-Item -ItemType Directory -Force -Path $Tools, $Runtime | Out-Null

function Fail([string]$Message) {
    Write-Host ""
    Write-Host "오류: $Message" -ForegroundColor Red
    Write-Host ""
    Write-Host "이 창의 내용을 캡처해서 ChatGPT에 보내주시면 됩니다."
    Read-Host "Enter를 누르면 닫힙니다"
    exit 1
}

function Need-GoDownload {
    if (-not (Test-Path $GoExe)) { return $true }
    if (-not (Test-Path $GoVersionMarker)) { return $true }
    $marked = (Get-Content $GoVersionMarker -Raw).Trim()
    return ($marked -ne $GoVersion)
}

try {
    Write-Host "==============================================" -ForegroundColor Cyan
    Write-Host " tripleS Objekt Bulk Sender v4 + SPIN - 첫 실행 준비" -ForegroundColor Cyan
    Write-Host "==============================================" -ForegroundColor Cyan
    Write-Host ""

    if (Need-GoDownload) {
        Write-Host "[1/3] 필요한 실행 파일을 자동으로 받고 있습니다..."
        Write-Host "      별도로 설치하거나 설정하실 것은 없습니다."
        if (Test-Path $GoZip) { Remove-Item $GoZip -Force }
        if (Test-Path $GoDir) { Remove-Item $GoDir -Recurse -Force }
        if (Test-Path $GoVersionMarker) { Remove-Item $GoVersionMarker -Force }

        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri $GoUrl -OutFile $GoZip -UseBasicParsing
        $actualHash = (Get-FileHash -Path $GoZip -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $GoSha256) {
            Remove-Item $GoZip -Force -ErrorAction SilentlyContinue
            Fail "받은 실행 파일 검증에 실패했습니다. 다시 START.bat을 실행해 주세요."
        }
        Expand-Archive -Path $GoZip -DestinationPath $Tools -Force
        Remove-Item $GoZip -Force
        Set-Content -Path $GoVersionMarker -Value $GoVersion -NoNewline -Encoding ASCII
    } else {
        Write-Host "[1/3] 필요한 실행 파일 확인 완료 (Go $GoVersion)"
    }

    $env:PATH = "$(Join-Path $GoDir 'bin');$env:PATH"
    $env:GOTOOLCHAIN = "local"
    $env:GOPROXY = "https://proxy.golang.org,direct"
    Write-Host "      $((& $GoExe version) -join ' ')"

    Write-Host "[2/3] Cosmo 모듈과 필요한 라이브러리를 준비하고 있습니다..."
    if (-not (Test-Path (Join-Path $Root "cosmo-module\internal\cosmo\webbridge_crypto_export.go"))) {
        Fail "ZIP 안의 Cosmo 로컬 모듈이 누락되었습니다."
    }
    & $GoExe mod download all
    if ($LASTEXITCODE -ne 0) { Fail "필요한 Go 라이브러리 준비에 실패했습니다. 위 오류 내용을 캡처해 주세요." }

    Write-Host "[3/3] 실행 프로그램을 만드는 중입니다..."
    & $GoExe build -mod=mod -trimpath -ldflags="-s -w" -o (Join-Path $Runtime "localserver.exe") .\localserver
    if ($LASTEXITCODE -ne 0) { Fail "실행 프로그램 생성에 실패했습니다. 위 오류 내용을 캡처해 주세요." }
    Set-Content -Path (Join-Path $Runtime "build-version.txt") -Value $BuildVersion -NoNewline -Encoding ASCII

    Write-Host ""
    Write-Host "준비 완료! 이제부터는 START.bat만 더블클릭하면 됩니다." -ForegroundColor Green
    Write-Host "브라우저를 여는 중입니다..." -ForegroundColor Green
    Start-Process (Join-Path $Runtime "localserver.exe")
    Start-Sleep -Milliseconds 800
    exit 0
}
catch {
    Fail $_.Exception.Message
}
