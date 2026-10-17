import { createHash } from 'node:crypto';
import { promises as fs } from 'node:fs';
import path from 'node:path';

/** 同路径写互斥：tmp 名含 Date.now()，同毫秒并发写同一文件会生成同名 tmp——
 * 先完成的 rename 把文件移走，后一个 rename 必 ENOENT。按目标路径排队串行，
 * 配合"内存单实例 + 全量落盘"的保存模式，串行后的 last-write-wins 即正确结果。 */
const writeQueues = new Map<string, Promise<unknown>>();

export async function atomicWriteFile(filePath: string, data: string): Promise<void> {
  const key = path.resolve(filePath);
  const job = (writeQueues.get(key) ?? Promise.resolve())
    .catch(() => {})
    .then(() => atomicWriteFileInner(filePath, data));
  writeQueues.set(key, job);
  try {
    await job;
  } finally {
    if (writeQueues.get(key) === job) writeQueues.delete(key);
  }
}

/** 原子写：临时文件 + 同卷 rename；成功后目录内不残留临时文件。
 * Windows 上 rename 覆盖已有文件可能因目标被短暂占用（杀软/索引器）抛 EPERM/EBUSY，
 * 采用指数退避重试——这是发布到用户机器的必要健壮性。 */
async function atomicWriteFileInner(filePath: string, data: string): Promise<void> {
  const dir = path.dirname(filePath);
  await fs.mkdir(dir, { recursive: true });
  const tmp = path.join(dir, `.${path.basename(filePath)}.${process.pid}.${Date.now()}.tmp`);
  try {
    await fs.writeFile(tmp, data, 'utf8');
    // S0 持久化 stage：rename 前 fsync 临时文件，掉电后意图恢复才成立
    // （Windows 的 FlushFileBuffers 要求句柄带写权限，'r' 会 EPERM，必须 'r+'）
    const fh = await fs.open(tmp, 'r+');
    try {
      await fh.sync();
    } finally {
      await fh.close();
    }
    let lastError: unknown;
    for (let attempt = 0; attempt < 5; attempt++) {
      try {
        await fs.rename(tmp, filePath);
        // POSIX 上再 fsync 目录使 rename 本身持久化；Windows 不支持打开目录，跳过
        if (process.platform !== 'win32') {
          try {
            const dh = await fs.open(dir, 'r');
            try {
              await dh.sync();
            } finally {
              await dh.close();
            }
          } catch {
            // 目录 fsync 失败不影响写入正确性
          }
        }
        return;
      } catch (err) {
        lastError = err;
        const code = (err as NodeJS.ErrnoException).code;
        const transient =
          code === 'EPERM' || code === 'EBUSY' || code === 'EACCES';
        if (!transient || attempt === 4) break;
        await new Promise((resolve) => setTimeout(resolve, 10 * 2 ** attempt));
      }
    }
    throw lastError;
  } catch (err) {
    await fs.rm(tmp, { force: true }).catch(() => {});
    throw err;
  }
}

export async function readTextIfExists(filePath: string): Promise<string | null> {
  try {
    return await fs.readFile(filePath, 'utf8');
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return null;
    throw err;
  }
}

/**
 * 确定性序列化：对象键递归排序、数组保序、undefined 字段剔除。
 * 用于"管辖片段"的内容 hash——同内容不同键序必须得到相同 hash。
 */
export function stableStringify(value: unknown): string {
  return JSON.stringify(sortValue(value)) ?? 'null';
}

function sortValue(value: unknown): unknown {
  if (value === null || typeof value !== 'object') return value;
  if (Array.isArray(value)) return value.map(sortValue);
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(value as Record<string, unknown>).sort()) {
    const v = (value as Record<string, unknown>)[key];
    if (v !== undefined) out[key] = sortValue(v);
  }
  return out;
}

export function stableHash(value: unknown): string {
  return createHash('sha256').update(stableStringify(value)).digest('hex');
}
