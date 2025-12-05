#!/bin/bash

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
pushd "${SCRIPT_DIR}"

ARCHIVE_URL="https://git.gammaspectra.live/P2Pool/p2pool/raw/commit/"

# Pre-v2 p2pool hardfork
TESTS_COMMIT_ID_V1=b9eb66e2b3e02a5ec358ff8a0c5169a5606d9fde
function download_test_v1() {
    if [ -f "./v1_${1}" ]; then
      return
    fi
    curl --progress-bar --output "./v1_${1}" "${ARCHIVE_URL}${TESTS_COMMIT_ID_V1}/tests/src/${1}"
}

# Post-v2 p2pool hardfork
TESTS_COMMIT_ID_V2=f455ce398c20137a92a67b062c6311580939abea
function download_test_v2() {
    if [ -f "./v2_${1}" ]; then
      return
    fi
    curl --progress-bar --output "./v2_${1}" "${ARCHIVE_URL}${TESTS_COMMIT_ID_V2}/tests/src/${1}"
}

# Post-v4 p2pool hardfork
TESTS_COMMIT_ID_V4=547b3430c247237c8a680869a4334a68a4bd41d5
function download_test_v4() {
    if [ -f "./v4_${1}" ]; then
      return
    fi
    curl --progress-bar --output "./v4_${1}" "${ARCHIVE_URL}${TESTS_COMMIT_ID_V4}/tests/src/${1}"
}

# Post-v4 p2pool hardfork, updated tests
TESTS_COMMIT_ID_V4_2=0755a9dcf199b9568dee06cf24117c59b92d8a1d
function download_test_v4_2() {
    if [ -f "./v4_${1}" ]; then
      return
    fi
    curl --progress-bar --output "./v4_2_${1}" "${ARCHIVE_URL}${TESTS_COMMIT_ID_V4_2}/tests/src/${1}"
}

download_test_v4_2 sidechain_dump.dat.xz
download_test_v4_2 sidechain_dump_mini.dat.xz
download_test_v4_2 sidechain_dump_nano.dat.xz

download_test_v4 block.dat
download_test_v4 sidechain_dump.dat.gz
download_test_v4 sidechain_dump_mini.dat.gz
download_test_v4 sidechain_dump_nano.dat.gz

download_test_v2 block.dat
download_test_v2 crypto_tests.txt
download_test_v2 sidechain_dump.dat.gz
download_test_v2 sidechain_dump_mini.dat.gz

download_test_v1 mainnet_test2_block.dat
download_test_v1 sidechain_dump.dat
download_test_v1 sidechain_dump_mini.dat

sha256sum -c testdata.sha256