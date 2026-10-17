import { promises as fs } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { atomicWriteFile } from '../src/fsutil.js';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('atomicWriteFile 并发写同一文件', () => {
  // tmp 名含 Date.now()：钉成常量即全部同名——无队列的旧实现第一个 rename 移走文件后必 ENOENT
  it('同毫秒并发不丢写、不残留临时文件，落盘为完整内容之一', async () => {
    vi.spyOn(Date, 'now').mockReturnValue(1700000000000);
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'beaver-fsutil-'));
    const file = path.join(dir, 'config.json');
    await atomicWriteFile(file, '{"v":0}\n'); // 先让 rename 目标存在（覆盖路径）
    const writes = Array.from({ length: 32 }, (_, i) => atomicWriteFile(file, `{"v":${i + 1}}\n`));
    await expect(Promise.all(writes)).resolves.toHaveLength(32);
    const text = await fs.readFile(file, 'utf8');
    expect(text).toMatch(/^\{"v":\d+\}\n$/);
    const leftovers = (await fs.readdir(dir)).filter((n) => n.endsWith('.tmp'));
    expect(leftovers).toEqual([]);
    await fs.rm(dir, { recursive: true, force: true });
  });
});
