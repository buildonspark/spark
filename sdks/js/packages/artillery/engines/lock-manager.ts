import Database from "better-sqlite3";
import * as path from "path";
import * as fs from "fs";

// Singleton lock manager using SQLite
export class LockManager {
  private static instance: LockManager;
  private db: Database.Database;
  private initialized: boolean = false;
  private cleanupInterval: NodeJS.Timeout | null = null;

  private constructor() {
    const dbDir =
      process.env.ARTILLERY_LOCK_DB_DIR ||
      path.join(process.cwd(), ".artillery-locks-db");

    if (!fs.existsSync(dbDir)) {
      fs.mkdirSync(dbDir, { recursive: true });
    }

    const dbPath = path.join(dbDir, "locks.db");

    this.db = new Database(dbPath);

    this.db.pragma("journal_mode = WAL");

    this.db.pragma("busy_timeout = 5000");

    this.initializeDatabase();

    this.startCleanupTask();
  }

  static getInstance(): LockManager {
    if (!LockManager.instance) {
      LockManager.instance = new LockManager();
    }
    return LockManager.instance;
  }

  private initializeDatabase(): void {
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS locks (
        address TEXT PRIMARY KEY,
        metadata TEXT NOT NULL,
        locked_at INTEGER NOT NULL,
        pid INTEGER NOT NULL
      )
    `);

    this.db.exec(`
      CREATE INDEX IF NOT EXISTS idx_locked_at ON locks(locked_at)
    `);

    this.initialized = true;
    console.log("SQLite lock manager initialized");
  }

  private startCleanupTask(): void {
    this.cleanupInterval = setInterval(
      () => {
        this.cleanupStaleLocks().catch((err) =>
          console.error("Periodic cleanup failed:", err),
        );
      },
      5 * 60 * 1000,
    );
  }

  async createLock(address: string, metadata: any = {}): Promise<boolean> {
    try {
      const stmt = this.db.prepare(`
        INSERT INTO locks (address, metadata, locked_at, pid)
        VALUES (?, ?, ?, ?)
      `);

      const lockData = {
        ...metadata,
        lockedAt: new Date().toISOString(),
        pid: process.pid,
      };

      stmt.run(address, JSON.stringify(lockData), Date.now(), process.pid);

      return true;
    } catch (error) {
      if (error.code === "SQLITE_CONSTRAINT_PRIMARYKEY") {
        return false; // Lock already exists
      }
      console.error(`Failed to create lock for ${address}:`, error);
      return false;
    }
  }

  async removeLock(address: string): Promise<boolean> {
    try {
      const stmt = this.db.prepare("DELETE FROM locks WHERE address = ?");
      const result = stmt.run(address);
      return result.changes > 0;
    } catch (error) {
      console.error(`Failed to remove lock for ${address}:`, error);
      return false;
    }
  }

  async isLocked(address: string): Promise<boolean> {
    try {
      const stmt = this.db.prepare("SELECT 1 FROM locks WHERE address = ?");
      const result = stmt.get(address);
      return !!result;
    } catch (error) {
      console.error(`Failed to check lock for ${address}:`, error);
      throw error;
    }
  }

  async cleanupStaleLocks(maxAge: number = 3600000): Promise<void> {
    const cutoffTime = Date.now() - maxAge;

    try {
      const selectStmt = this.db.prepare(
        "SELECT address FROM locks WHERE locked_at < ?",
      );
      const staleLocks = selectStmt.all(cutoffTime);

      if (staleLocks.length > 0) {
        const deleteStmt = this.db.prepare(
          "DELETE FROM locks WHERE locked_at < ?",
        );
        const result = deleteStmt.run(cutoffTime);

        console.log(`Cleaned up ${result.changes} stale locks`);

        if (staleLocks.length <= 10) {
          staleLocks.forEach((lock: any) => {
            console.log(`  - Removed stale lock: ${lock.address}`);
          });
        }
      }

      this.cleanupDeadProcessLocks();
    } catch (error) {
      console.error("Failed to cleanup stale locks:", error);
    }
  }

  private cleanupDeadProcessLocks(): void {
    try {
      const pidStmt = this.db.prepare("SELECT DISTINCT pid FROM locks");
      const pids = pidStmt.all();

      const deadPids: number[] = [];

      for (const row of pids as { pid: number }[]) {
        const pid = row.pid;
        if (!this.isProcessAlive(pid)) {
          deadPids.push(pid);
        }
      }

      if (deadPids.length > 0) {
        const deleteStmt = this.db.prepare("DELETE FROM locks WHERE pid = ?");
        let removedCount = 0;

        for (const pid of deadPids) {
          const result = deleteStmt.run(pid);
          removedCount += result.changes;
        }

        if (removedCount > 0) {
          console.log(
            `Cleaned up ${removedCount} locks from ${deadPids.length} dead processes`,
          );
        }
      }
    } catch (error) {
      console.error("Failed to cleanup dead process locks:", error);
    }
  }

  private isProcessAlive(pid: number): boolean {
    try {
      process.kill(pid, 0);
      return true;
    } catch (error) {
      return false;
    }
  }

  async close(): Promise<void> {
    if (this.cleanupInterval) {
      clearInterval(this.cleanupInterval);
      this.cleanupInterval = null;
    }

    if (this.db) {
      this.db.close();
    }

    this.initialized = false;
  }

  async getAllLocks(): Promise<Map<string, any>> {
    const locks = new Map<string, any>();

    try {
      const stmt = this.db.prepare("SELECT address, metadata FROM locks");
      const rows = stmt.all() as { address: string; metadata: string }[];

      for (const row of rows) {
        try {
          locks.set(row.address, JSON.parse(row.metadata));
        } catch (parseError) {
          locks.set(row.address, row.metadata);
        }
      }
    } catch (error) {
      console.error("Failed to get all locks:", error);
    }

    return locks;
  }

  async getLockCount(): Promise<number> {
    try {
      const stmt = this.db.prepare("SELECT COUNT(*) as count FROM locks");
      const result = stmt.get() as { count: number };
      return result.count;
    } catch (error) {
      console.error("Failed to get lock count:", error);
      return 0;
    }
  }

  async tryLockOneOf(
    addresses: string[],
    metadata: any = {},
  ): Promise<string | null> {
    const db = this.db;
    const startTime = Date.now();

    const transaction = db.transaction((addrs: string[], meta: any) => {
      const txStartTime = Date.now();
      let attempts = 0;

      for (const address of addrs) {
        attempts++;
        try {
          const insertStartTime = Date.now();
          const stmt = db.prepare(`
            INSERT INTO locks (address, metadata, locked_at, pid)
            VALUES (?, ?, ?, ?)
          `);

          const lockData = {
            ...meta,
            lockedAt: new Date().toISOString(),
            pid: process.pid,
          };

          stmt.run(address, JSON.stringify(lockData), Date.now(), process.pid);

          const insertTime = Date.now() - insertStartTime;
          const totalTxTime = Date.now() - txStartTime;
          console.log(
            `    [LOCK TIMING] Successfully locked address after ${attempts} attempts. Insert: ${insertTime}ms, Total TX: ${totalTxTime}ms`,
          );

          return address;
        } catch (error) {
          if (error.code === "SQLITE_CONSTRAINT_PRIMARYKEY") {
            continue;
          }
          throw error;
        }
      }

      const totalTxTime = Date.now() - txStartTime;
      console.log(
        `    [LOCK TIMING] Failed to lock any address after ${attempts} attempts. Total TX: ${totalTxTime}ms`,
      );

      return null;
    });

    try {
      const result = transaction(addresses, metadata);
      const totalTime = Date.now() - startTime;

      if (result) {
        console.log(
          `    [LOCK TIMING] tryLockOneOf completed successfully. Total time: ${totalTime}ms`,
        );
      } else {
        console.log(
          `    [LOCK TIMING] tryLockOneOf failed (all addresses locked). Total time: ${totalTime}ms for ${addresses.length} addresses`,
        );
      }

      return result;
    } catch (error) {
      const totalTime = Date.now() - startTime;
      console.error(
        `Failed to atomically lock addresses (${totalTime}ms):`,
        error,
      );
      return null;
    }
  }

  async getUnlockedAddresses(addresses: string[]): Promise<string[]> {
    if (addresses.length === 0) return [];

    try {
      const placeholders = addresses.map(() => "?").join(",");
      const query = `SELECT address FROM locks WHERE address IN (${placeholders})`;

      const stmt = this.db.prepare(query);
      const lockedAddresses = stmt.all(...addresses) as { address: string }[];
      const lockedSet = new Set(lockedAddresses.map((row) => row.address));

      return addresses.filter((addr) => !lockedSet.has(addr));
    } catch (error) {
      console.error("Failed to get unlocked addresses:", error);
      return [];
    }
  }
}
