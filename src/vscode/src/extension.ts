// The G++ VS Code extension. Highlighting and editor behaviour (comments,
// brackets) are declarative (see package.json); this file starts the
// language server, `gpp lsp`, for everything else.
import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  context.subscriptions.push(
    vscode.commands.registerCommand("gpp.restartServer", restart),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("gpp.serverPath")) {
        void restart();
      }
    }),
  );
  await start();
}

export async function deactivate(): Promise<void> {
  await stop();
}

async function start(): Promise<void> {
  const command = vscode.workspace.getConfiguration("gpp").get<string>("serverPath") || "gpp";
  const serverOptions: ServerOptions = { command, args: ["lsp"] };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "gpp" }],
    synchronize: {
      // Re-check when sources or the module change outside the editor.
      fileEvents: vscode.workspace.createFileSystemWatcher("**/{*.gpp,*.go,go.mod,go.sum}"),
    },
  };
  client = new LanguageClient("gpp", "G++ Language Server", serverOptions, clientOptions);
  try {
    await client.start();
  } catch (err) {
    client = undefined;
    void vscode.window.showErrorMessage(
      `Could not start the G++ language server ("${command} lsp"): ${err}. ` +
        `Install gpp or set "gpp.serverPath" in your settings.`,
    );
  }
}

async function stop(): Promise<void> {
  const c = client;
  client = undefined;
  if (c) {
    await c.stop();
  }
}

async function restart(): Promise<void> {
  await stop();
  await start();
}
