#!/bin/bash
cd "$(dirname "$0")"
EXPECTED_BUILD="v4-spin-20260925-5"
NEED_BUILD=0

# 런타임 및 빌드 버전 확인
if [ ! -f ".runtime/localserver" ] || [ ! -f ".runtime/build-version.txt" ]; then
    NEED_BUILD=1
else
    BUILT=$(cat ".runtime/build-version.txt")
    if [ "$BUILT" != "$EXPECTED_BUILD" ]; then
        NEED_BUILD=1
    fi
fi

if [ "$NEED_BUILD" -eq 1 ]; then
    echo "=============================================="
    echo " tripleS Cosmo Tool - 첫 실행 세팅 및 빌드"
    echo "=============================================="
    
    # Go 설치 여부 확인
    if ! command -v go &> /dev/null; then
        echo "오류: Mac에 Go 언어가 설치되어 있지 않습니다."
        echo "터미널을 열고 'brew install go'를 입력해 설치한 뒤 다시 실행해주세요."
        exit 1
    fi

    mkdir -p .runtime
    echo "필요한 모듈을 다운로드합니다..."
    go mod download all
    
    echo "실행 서버를 컴파일하는 중입니다..."
    go build -mod=mod -trimpath -ldflags="-s -w" -o .runtime/localserver ./localserver
    echo "$EXPECTED_BUILD" > .runtime/build-version.txt
    echo "빌드 완료!"
fi

echo "서버를 실행합니다..."
./.runtime/localserver