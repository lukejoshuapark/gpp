#!/bin/bash
# Installs the gpp command and the VS Code extension.
set -euo pipefail

cd "$(dirname "$0")"
go install github.com/lukejoshuapark/gpp/src/toolchain/cmd/gpp@latest
(cd src/vscode && npm install && npm run package && code --install-extension gpp-0.2.0.vsix)
echo "-- Success --"
