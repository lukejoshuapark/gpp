#!/bin/bash
# Installs the gpp command and the VS Code extension.
set -euo pipefail

cd "$(dirname "$0")"
(cd src/toolchain && go install ./cmd/gpp)
(cd src/vscode && npm install && npm run package && code --install-extension gpp-0.1.0.vsix)
echo "-- Success --"
