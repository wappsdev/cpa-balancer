#!/bin/sh
# Runs the proof tests inside a scratch copy of the CLIProxyAPI module this plugin builds against:
# they need the host's internal packages. The module cache is not touched.
#   - extramodel_test.go: the registrar, the scheduler pin and its reject (sdk/cliproxy);
#   - advisor_handler_test.go: an advisor only in the body, through the API handler, the request
#     interceptors and the scheduler (sdk/api/handlers);
#   - advisor_body_test.go: the plugin's body corpus, as CPA's claude executor reads it.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
version=$(cd "$here/../.." && go list -m -f '{{.Version}}' github.com/router-for-me/CLIProxyAPI/v8)
src=$(cd "$here/../.." && go list -m -f '{{.Dir}}' github.com/router-for-me/CLIProxyAPI/v8)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" && rm -rf "$scratch"' EXIT
cp -R "$src/." "$scratch/"
chmod -R u+w "$scratch"
cp "$here/extramodel_test.go" "$scratch/sdk/cliproxy/zz_extramodel_proof_test.go"
cp "$here/advisor_handler_test.go" "$scratch/sdk/api/handlers/zz_advisor_handler_proof_test.go"
cp "$here/advisor_body_test.go" "$scratch/internal/runtime/executor/zz_advisor_body_proof_test.go"
cp "$here/advisor_bodies.json" "$scratch/internal/runtime/executor/zz_advisor_bodies.json"
echo "CLIProxyAPI $version"
cd "$scratch"
go test -tags sdkproof -count=1 -run TestProofExtraModelThroughRegistrarAndScheduler -v ./sdk/cliproxy/
go test -tags sdkproof -count=1 -run TestProofAnAdvisorOnlyInTheBodyIsPinnedThroughTheHandler -v ./sdk/api/handlers/
go test -tags sdkproof -count=1 -run 'TestProofAdvisorBodiesAsCPAReadsThem|TestProofTheClaudeExecutorDoesNotForwardTheAdvisorMarker' -v ./internal/runtime/executor/
