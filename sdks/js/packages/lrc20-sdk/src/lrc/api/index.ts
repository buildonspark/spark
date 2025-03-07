export { ElectrsApi } from "./electrs.ts";
export { Lrc20JsonRPC } from "./lrc20.ts";
export type { JsonRpcAuth } from "./lrc20.ts";

export interface BasicAuth {
  username: string;
  password: string;
}

export function basicAuth(auth: BasicAuth): string {
  const { username, password } = auth;
  return Buffer.from(`${username}:${password}`).toString("base64");
}
