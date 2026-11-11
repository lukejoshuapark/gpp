// The G++ VS Code extension. Highlighting and editor behaviour (comments,
// brackets) are declarative (see package.json); this file starts the
// language server, `gpp lsp`, for everything else.
import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  context.subscriptions.push(
    vscode.debug.registerDebugConfigurationProvider("gpp", new DebugProvider()),
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

// Debugging: the "gpp" debug type builds the program with gpp (the Go
// extension's debugger builds with go, which knows nothing of .gpp files),
// then debugs the binary with the Go extension's Delve integration. The
// binary's line tables point at the .gpp sources.
class DebugProvider implements vscode.DebugConfigurationProvider {
  async resolveDebugConfigurationWithSubstitutedVariables(
    folder: vscode.WorkspaceFolder | undefined,
    config: vscode.DebugConfiguration,
  ): Promise<vscode.DebugConfiguration | undefined> {
    if (!config.type && !config.request && !config.name) {
      // F5 without a launch.json: debug the package of the open file
      const editor = vscode.window.activeTextEditor;
      if (!editor || editor.document.languageId !== "gpp") {
        return config;
      }
      config = { type: "gpp", request: "launch", name: "Debug G++ program", program: path.dirname(editor.document.fileName) };
    }
    if (!vscode.extensions.getExtension("golang.go")) {
      void vscode.window.showErrorMessage(
        "Debugging G++ uses the Go extension (golang.go) and Delve: install them, then try again.",
      );
      return undefined;
    }
    let program: string = config.program || folder?.uri.fsPath || process.cwd();
    let pkgDir = program;
    if (program.endsWith(".gpp") || program.endsWith(".go")) {
      pkgDir = path.dirname(program);
    }
    const out = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "gpp-debug-")), "__debug_bin" + (process.platform === "win32" ? ".exe" : ""));
    const gpp = vscode.workspace.getConfiguration("gpp").get<string>("serverPath") || "gpp";
    const args = ["build", "-gcflags=all=-N -l", "-o", out, ...(config.buildFlags ?? []), "."];
    try {
      await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Window, title: "gpp build" },
        () =>
          new Promise<void>((resolve, reject) => {
            execFile(gpp, args, { cwd: pkgDir }, (err, _stdout, stderr) => (err ? reject(new Error(stderr || err.message)) : resolve()));
          }),
      );
    } catch (err) {
      output().appendLine(String((err as Error).message));
      output().show(true);
      void vscode.window.showErrorMessage("gpp build failed: see the G++ output for details.");
      return undefined;
    }
    await vscode.debug.startDebugging(folder, {
      type: "go",
      request: "launch",
      mode: "exec",
      name: config.name,
      program: out,
      args: config.args ?? [],
      cwd: config.cwd ?? pkgDir,
      env: config.env ?? {},
    });
    return undefined; // the Go debug session replaces this one
  }
}

let outputChannel: vscode.OutputChannel | undefined;

function output(): vscode.OutputChannel {
  outputChannel ??= vscode.window.createOutputChannel("G++");
  return outputChannel;
}
