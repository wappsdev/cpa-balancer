#!/bin/sh
# Runs extramodel_test.go inside a scratch copy of the CLIProxyAPI module this plugin builds
# against: the test needs the host's internal packages. The module cache is not touched.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
version=$(cd "$here/../.." && go list -m -f '{{.Version}}' github.com/router-for-me/CLIProxyAPI/v8)
src=$(cd "$here/../.." && go list -m -f '{{.Dir}}' github.com/router-for-me/CLIProxyAPI/v8)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" && rm -rf "$scratch"' EXIT
cp -R "$src/." "$scratch/"
chmod -R u+w "$scratch"
cp "$here/extramodel_test.go" "$scratch/sdk/cliproxy/zz_extramodel_proof_test.go"
echo "CLIProxyAPI $version"
cd "$scratch"
go test -tags sdkproof -count=1 -run TestProofExtraModelThroughRegistrarAndScheduler -v ./sdk/cliproxy/
