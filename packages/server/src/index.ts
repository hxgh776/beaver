import { promises as fs } from 'node:fs';
import { Daemon, DAEMON_VERSION } from './daemon.js';

async function main(): Promise<void> {
  const args = process.argv.slice(2);
  let homeDir: string | undefined;
  let agentConfigRoot: string | undefined;
  let port: number | undefined;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--home' && args[i + 1]) {
      homeDir = args[i + 1];
      i++;
    } else if (args[i] === '--agent-home' && args[i + 1]) {
      agentConfigRoot = args[i + 1];
      i++;
    } else if (args[i] === '--port' && args[i + 1]) {
      port = Number(args[i + 1]);
      i++;
    }
  }

  const daemon = new Daemon({ homeDir, agentConfigRoot, port });
  await daemon.start();

  await fs.writeFile(daemon.store.pidPath, String(process.pid), 'utf8');

  const shutdown = (signal: string): void => {
    void (async () => {
      daemon.logger.info(`received ${signal}, shutting down`);
      await daemon.stop();
      await fs.rm(daemon.store.pidPath, { force: true }).catch(() => {});
      process.exit(0);
    })();
  };
  process.on('SIGINT', () => shutdown('SIGINT'));
  process.on('SIGTERM', () => shutdown('SIGTERM'));

  console.log(`beaver daemon v${DAEMON_VERSION}`);
  console.log(`  API/UI : ${daemon.baseUrl}`);
  console.log(`  MCP    : ${daemon.baseUrl}/mcp (gateway 启动后可用)`);
  console.log(`  home   : ${daemon.store.homeDir}`);
}

void main().catch((err) => {
  console.error('daemon 启动失败:', err);
  process.exit(1);
});
