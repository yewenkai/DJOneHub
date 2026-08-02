#!/bin/zsh

set -eu

root="${0:A:h}"
build_root="${root}/.build"
output_root="${root}/dist"
app="${output_root}/DJOneHubNotifier.app"
cache_root="${build_root}/local-cache"

cd "${root}"
mkdir -p "${cache_root}/clang" "${cache_root}/swiftpm"
export CLANG_MODULE_CACHE_PATH="${cache_root}/clang"
export SWIFTPM_MODULECACHE_OVERRIDE="${cache_root}/clang"
export SWIFTPM_CUSTOM_CACHE_PATH="${cache_root}/swiftpm"
swift build --disable-sandbox -c release
"${build_root}/release/DJOneHubNotifier" --self-test

rm -rf "${app}"
mkdir -p "${app}/Contents/MacOS" "${app}/Contents/Resources"
cp "${build_root}/release/DJOneHubNotifier" "${app}/Contents/MacOS/DJOneHubNotifier"
cp "${root}/Info.plist" "${app}/Contents/Info.plist"
chmod 755 "${app}/Contents/MacOS/DJOneHubNotifier"
codesign --force --deep --sign - "${app}"
codesign --verify --deep --strict --verbose=2 "${app}"
plutil -lint "${app}/Contents/Info.plist"

print -r -- "${app}"
